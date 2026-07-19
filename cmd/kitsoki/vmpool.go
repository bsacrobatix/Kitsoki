package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"kitsoki/internal/capsule/vmpool"
)

// defaultVMPoolTokenEnv is the environment variable that, by default, holds
// the DigitalOcean API token used to construct the production Provisioner.
// It intentionally does not follow the "DO_..._API_TOKEN" naming a casual
// grep would expect; that is by design for this deployment.
const defaultVMPoolTokenEnv = "DO_GAGNRENOUS_TURD_API_KEY"

// vmpoolNewProvisioner constructs the Provisioner backing every vmpool
// subcommand. Production wires vmpool.NewDO from the --token-env variable;
// tests override this package hook to inject vmpool.NewFake() (or any other
// Provisioner) so the command tree can be exercised with no network access
// and no DigitalOcean account.
var vmpoolNewProvisioner = func(tokenEnv string) (vmpool.Provisioner, error) {
	token := strings.TrimSpace(os.Getenv(tokenEnv))
	if token == "" {
		return nil, fmt.Errorf("vmpool: environment variable %s is not set; export a DigitalOcean API token or pass --token-env", tokenEnv)
	}
	return vmpool.NewDO(token), nil
}

// vmpoolNow stamps image-builder timestamps and names; tests override it for
// deterministic instance names.
var vmpoolNow = func() time.Time { return time.Now().UTC() }

const (
	vmpoolBuilderTag        = "kitsoki-image-builder"
	vmpoolDefaultBaseImage  = "ubuntu-22-04-x64"
	vmpoolFallbackGoVersion = "1.25.0"
	vmpoolImageBuilderFile  = "image-builder.json"
	vmpoolVerifyScriptPath  = "/usr/local/bin/kitsoki-image-verify"
	vmpoolSecurityNote      = "SECURITY: the resulting snapshot embeds live, authenticated agent credentials (claude/codex). Treat the snapshot like a secret: it must stay private to this DigitalOcean account. There is no in-place credential rotation for a snapshot — rotate by rebuilding a fresh image."
	vmpoolImageBuildDoc     = "`vmpool image build` boots an interactive builder droplet and prints the SSH command for a human login-assist session; it never completes unattended, because claude/codex authentication requires an interactive login. Run `vmpool image finalize` once " + vmpoolVerifyScriptPath + " reports VERIFIED on the builder.\n\n" + vmpoolSecurityNote
)

// vmpoolCmd is the `kitsoki vmpool` command group: durable pool operations
// (status/release/reap) plus the base-image authoring workflow
// (image build/finalize/list).
func vmpoolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vmpool",
		Short: "Manage the ephemeral-worker VM pool and its base image",
		Long: "Operate the DigitalOcean-backed ephemeral-worker pool (internal/capsule/vmpool): inspect and reconcile\n" +
			"durable pool state, and author the base image every worker boots from.\n\n" + vmpoolSecurityNote,
	}
	cmd.AddCommand(vmpoolStatusCmd(), vmpoolReleaseCmd(), vmpoolReapCmd(), vmpoolImageCmd(), vmpoolSmokeCmd())
	return cmd
}

// vmpoolCommonFlags are shared by every vmpool subcommand: where the durable
// store lives, and how to authenticate to the cloud provider.
type vmpoolCommonFlags struct {
	project  string
	tokenEnv string
}

func addVMPoolCommonFlags(cmd *cobra.Command, f *vmpoolCommonFlags) {
	cmd.Flags().StringVar(&f.project, "project", ".", "project root containing the durable pool store (.capsules/vmpool)")
	cmd.Flags().StringVar(&f.tokenEnv, "token-env", defaultVMPoolTokenEnv, "environment variable holding the DigitalOcean API token")
}

// vmpoolConfigFlags mirror vmpool.Config, letting each pool subcommand
// resolve and (for status) display the effective Config. Unset fields default
// via vmpool.Config.WithDefaults.
type vmpoolConfigFlags struct {
	tag              string
	namePrefix       string
	maxConcurrent    int
	region           string
	size             string
	image            string
	vpcUUID          string
	sshKeyIDs        []string
	provisionTimeout time.Duration
	activityTimeout  time.Duration
	maxLifetime      time.Duration
}

func addVMPoolConfigFlags(cmd *cobra.Command, f *vmpoolConfigFlags) {
	cmd.Flags().StringVar(&f.tag, "tag", "", "pool droplet tag (default "+vmpool.DefaultTag+")")
	cmd.Flags().StringVar(&f.namePrefix, "name-prefix", "", "worker instance name prefix (default "+vmpool.DefaultNamePrefix+")")
	cmd.Flags().IntVar(&f.maxConcurrent, "max-concurrent", 0, fmt.Sprintf("max non-terminal workers (default %d)", vmpool.DefaultMaxConcurrent))
	cmd.Flags().StringVar(&f.region, "region", "", "DO region slug (default "+vmpool.DefaultRegion+")")
	cmd.Flags().StringVar(&f.size, "size", "", "DO droplet size slug (default "+vmpool.DefaultSize+")")
	cmd.Flags().StringVar(&f.image, "image", "", "worker base image: snapshot name/ID or distribution slug")
	cmd.Flags().StringVar(&f.vpcUUID, "vpc-uuid", "", "DO VPC UUID")
	cmd.Flags().StringSliceVar(&f.sshKeyIDs, "ssh-key-id", nil, "DO SSH key id installed on new worker instances (repeatable)")
	cmd.Flags().DurationVar(&f.provisionTimeout, "provision-timeout", 0, fmt.Sprintf("provisioning timeout (default %s)", vmpool.DefaultProvisionTimeout))
	cmd.Flags().DurationVar(&f.activityTimeout, "activity-timeout", 0, fmt.Sprintf("activity timeout (default %s)", vmpool.DefaultActivityTimeout))
	cmd.Flags().DurationVar(&f.maxLifetime, "max-lifetime", 0, fmt.Sprintf("max worker lifetime (default %s)", vmpool.DefaultMaxLifetime))
}

func (f vmpoolConfigFlags) config() vmpool.Config {
	return vmpool.Config{
		Tag:              f.tag,
		NamePrefix:       f.namePrefix,
		MaxConcurrent:    f.maxConcurrent,
		Region:           f.region,
		Size:             f.size,
		Image:            f.image,
		VPCUUID:          f.vpcUUID,
		SSHKeyIDs:        f.sshKeyIDs,
		ProvisionTimeout: f.provisionTimeout,
		ActivityTimeout:  f.activityTimeout,
		MaxLifetime:      f.maxLifetime,
	}.WithDefaults()
}

// vmpoolSmokeCmd is the live worker-plane proof: lease one ephemeral worker
// from the configured base image, wait for its worker service to answer the
// authenticated capabilities probe over pinned TLS, then release/destroy it.
// This is a paid operation (one droplet for a few minutes) and the sanctioned
// preflight before pointing dispatch at a new image.
func vmpoolSmokeCmd() *cobra.Command {
	var common vmpoolCommonFlags
	var cfgFlags vmpoolConfigFlags
	var jobID string
	var keep, preserveFailed bool
	cmd := &cobra.Command{
		Use:          "smoke",
		Short:        "Boot one ephemeral worker from the base image, verify its worker service, destroy it",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			pool, err := vmpoolBuildPool(common, cfgFlags)
			if err != nil {
				return err
			}
			pool.PreserveFailed = preserveFailed
			dispatcher := &vmpool.Dispatcher{Pool: pool}
			started := time.Now()
			lease, err := dispatcher.Lease(cmd.Context(), vmpool.LeaseSpec{JobID: jobID})
			if err != nil {
				return fmt.Errorf("vmpool smoke: %w", err)
			}
			caps, capsErr := lease.Remote.Describe(cmd.Context())
			result := map[string]any{
				"worker_id":    lease.Worker.ID,
				"instance":     lease.Worker.InstanceID,
				"endpoint":     lease.Endpoint,
				"ready_after":  time.Since(started).Round(time.Second).String(),
				"capabilities": caps,
			}
			if capsErr != nil {
				result["capabilities_error"] = capsErr.Error()
			}
			if !keep {
				if releaseErr := lease.Release(cmd.Context()); releaseErr != nil {
					result["release_error"] = releaseErr.Error()
				} else {
					result["released"] = true
				}
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
				return err
			}
			if capsErr != nil {
				return fmt.Errorf("vmpool smoke: worker service probe failed: %w", capsErr)
			}
			return nil
		},
	}
	addVMPoolCommonFlags(cmd, &common)
	addVMPoolConfigFlags(cmd, &cfgFlags)
	cmd.Flags().StringVar(&jobID, "job", "smoke", "job id for the smoke lease")
	cmd.Flags().BoolVar(&keep, "keep", false, "keep the worker running after the probe (release manually via vmpool release)")
	cmd.Flags().BoolVar(&preserveFailed, "preserve-failed", true, "on failure, keep the instance running for post-mortem (reclaim via vmpool release)")
	return cmd
}

// vmpoolBuildPool resolves the Provisioner and assembles a Pool over the
// durable store at common.project.
func vmpoolBuildPool(common vmpoolCommonFlags, cfg vmpoolConfigFlags) (*vmpool.Pool, error) {
	prov, err := vmpoolNewProvisioner(common.tokenEnv)
	if err != nil {
		return nil, err
	}
	return &vmpool.Pool{
		Store:       &vmpool.Store{ProjectRoot: common.project},
		Provisioner: prov,
		Config:      cfg.config(),
	}, nil
}

// vmpoolStatusOutput is the JSON shape printed by `vmpool status`.
type vmpoolStatusOutput struct {
	Config    vmpool.Config          `json:"config"`
	Workers   []vmpool.Worker        `json:"workers"`
	Reconcile vmpool.ReconcileReport `json:"reconcile"`
}

func vmpoolStatusCmd() *cobra.Command {
	var common vmpoolCommonFlags
	var cfgFlags vmpoolConfigFlags
	var jsonOut, repair bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show pool config, durable workers, and a live orphan/lost reconcile report",
		Long: "Prints the effective pool Config, every durable worker record, and a tag-based reconcile\n" +
			"report against the live cloud account. By default the reconcile is plan-only: orphaned\n" +
			"instances and lost workers are listed but not touched. Pass --repair to apply it (destroy\n" +
			"orphans, mark lost workers failed) via the same path `vmpool reap --repair` uses.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			pool, err := vmpoolBuildPool(common, cfgFlags)
			if err != nil {
				return err
			}
			workers, err := pool.List()
			if err != nil {
				return err
			}
			report, err := vmpoolReconcile(cmd.Context(), pool, repair)
			if err != nil {
				return err
			}
			out := vmpoolStatusOutput{Config: pool.Config, Workers: workers, Reconcile: report}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			return vmpoolPrintStatus(cmd.OutOrStdout(), out)
		},
	}
	addVMPoolCommonFlags(cmd, &common)
	addVMPoolConfigFlags(cmd, &cfgFlags)
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the full status as JSON")
	cmd.Flags().BoolVar(&repair, "repair", false, "apply the reconcile plan instead of only reporting it")
	return cmd
}

func vmpoolPrintStatus(w io.Writer, out vmpoolStatusOutput) error {
	if _, err := fmt.Fprintf(w, "tag=%s region=%s size=%s max_concurrent=%d image=%s\n",
		out.Config.Tag, out.Config.Region, out.Config.Size, out.Config.MaxConcurrent, ifEmpty(out.Config.Image, "(none)")); err != nil {
		return err
	}
	if len(out.Workers) == 0 {
		if _, err := fmt.Fprintln(w, "no durable workers"); err != nil {
			return err
		}
	}
	for _, wk := range out.Workers {
		if _, err := fmt.Fprintf(w, "worker %s job=%s status=%s instance=%s ip=%s\n",
			wk.ID, wk.JobID, wk.Status, ifEmpty(wk.InstanceID, "-"), ifEmpty(wk.PublicIP, "-")); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "reconcile: active=%d orphans=%d lost=%d\n",
		len(out.Reconcile.Active), len(out.Reconcile.Orphans), len(out.Reconcile.Lost)); err != nil {
		return err
	}
	for _, id := range out.Reconcile.Orphans {
		if _, err := fmt.Fprintf(w, "  orphan instance %s\n", id); err != nil {
			return err
		}
	}
	for _, id := range out.Reconcile.Lost {
		if _, err := fmt.Fprintf(w, "  lost worker %s\n", id); err != nil {
			return err
		}
	}
	return nil
}

// vmpoolReconcile runs the live tag-based reconcile. With repair=false it
// computes the same orphan/lost classification Pool.Reconcile does, but
// without destroying orphans or marking lost workers failed — a read-only
// plan safe to run at any time. With repair=true it delegates to
// Pool.Reconcile, which applies those actions.
func vmpoolReconcile(ctx context.Context, pool *vmpool.Pool, repair bool) (vmpool.ReconcileReport, error) {
	if repair {
		return pool.Reconcile(ctx)
	}
	return vmpoolPlanReconcile(ctx, pool)
}

func vmpoolPlanReconcile(ctx context.Context, pool *vmpool.Pool) (vmpool.ReconcileReport, error) {
	cfg := pool.Config.WithDefaults()
	state, err := pool.Store.Load()
	if err != nil {
		return vmpool.ReconcileReport{}, err
	}
	instances, err := pool.Provisioner.ListByTag(ctx, cfg.Tag)
	if err != nil {
		return vmpool.ReconcileReport{}, fmt.Errorf("vmpool: list instances by tag %s: %w", cfg.Tag, err)
	}

	knownInstance := make(map[string]bool, len(state.Workers))
	for _, w := range state.Workers {
		if !w.Status.Terminal() && w.InstanceID != "" {
			knownInstance[w.InstanceID] = true
		}
	}

	instanceExists := make(map[string]bool, len(instances))
	var report vmpool.ReconcileReport
	for _, inst := range instances {
		instanceExists[inst.ID] = true
		if knownInstance[inst.ID] {
			report.Active = append(report.Active, inst.ID)
			continue
		}
		report.Orphans = append(report.Orphans, inst.ID)
	}

	for _, w := range state.Workers {
		if w.Status.Terminal() || w.InstanceID == "" {
			continue
		}
		if instanceExists[w.InstanceID] {
			continue
		}
		report.Lost = append(report.Lost, w.ID)
	}
	return report, nil
}

func vmpoolReleaseCmd() *cobra.Command {
	var common vmpoolCommonFlags
	var cfgFlags vmpoolConfigFlags
	cmd := &cobra.Command{
		Use:   "release <worker-id>",
		Short: "Destroy a worker's instance (idempotent) and mark it destroyed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pool, err := vmpoolBuildPool(common, cfgFlags)
			if err != nil {
				return err
			}
			if err := pool.Release(cmd.Context(), args[0]); err != nil {
				return err
			}
			workers, err := pool.List()
			if err != nil {
				return err
			}
			w, _ := vmpoolFindWorker(workers, args[0])
			return json.NewEncoder(cmd.OutOrStdout()).Encode(w)
		},
	}
	addVMPoolCommonFlags(cmd, &common)
	addVMPoolConfigFlags(cmd, &cfgFlags)
	return cmd
}

func vmpoolFindWorker(workers []vmpool.Worker, id string) (vmpool.Worker, bool) {
	for _, w := range workers {
		if w.ID == id {
			return w, true
		}
	}
	return vmpool.Worker{}, false
}

func vmpoolReapCmd() *cobra.Command {
	var common vmpoolCommonFlags
	var cfgFlags vmpoolConfigFlags
	var repair bool
	cmd := &cobra.Command{
		Use:   "reap",
		Short: "Reconcile tagged cloud instances against the durable pool (plan-only unless --repair)",
		Long: "Lists cloud instances tagged for this pool that have no matching non-terminal durable worker\n" +
			"(orphans) and durable workers whose instance has disappeared (lost). Without --repair this is\n" +
			"a read-only report; with --repair, orphans are destroyed and lost workers are marked failed.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			pool, err := vmpoolBuildPool(common, cfgFlags)
			if err != nil {
				return err
			}
			report, err := vmpoolReconcile(cmd.Context(), pool, repair)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		},
	}
	addVMPoolCommonFlags(cmd, &common)
	addVMPoolConfigFlags(cmd, &cfgFlags)
	cmd.Flags().BoolVar(&repair, "repair", false, "apply the reconcile plan (destroy orphans, mark lost workers failed)")
	return cmd
}

// vmpoolImageCmd groups the base-image authoring workflow. Image authoring is
// interactive-with-SSH-handoff by design: claude/codex both require a human
// login, so no subcommand here fully automates image creation.
func vmpoolImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Author and inspect the ephemeral-worker base image",
		Long:  vmpoolImageBuildDoc,
	}
	cmd.AddCommand(vmpoolImageBuildCmd(), vmpoolImageFinalizeCmd(), vmpoolImageListCmd())
	return cmd
}

// vmpoolImageBuilder is the durable record of one image-builder droplet,
// persisted so `image finalize` can find the builder by name.
type vmpoolImageBuilder struct {
	DropletID    string    `json:"droplet_id"`
	Name         string    `json:"name"`
	CreatedAt    time.Time `json:"created_at"`
	SnapshotName string    `json:"snapshot_name"`
	Region       string    `json:"region,omitempty"`
	Size         string    `json:"size,omitempty"`
}

func vmpoolImageBuilderPath(project string) (string, error) {
	root, err := filepath.Abs(project)
	if err != nil {
		return "", fmt.Errorf("vmpool: resolve project root: %w", err)
	}
	return filepath.Join(root, ".capsules", "vmpool", vmpoolImageBuilderFile), nil
}

func vmpoolLoadImageBuilders(project string) ([]vmpoolImageBuilder, error) {
	path, err := vmpoolImageBuilderPath(project)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("vmpool: read image builders: %w", err)
	}
	var list []vmpoolImageBuilder
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("vmpool: parse image builders: %w", err)
	}
	return list, nil
}

func vmpoolSaveImageBuilders(project string, list []vmpoolImageBuilder) error {
	path, err := vmpoolImageBuilderPath(project)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("vmpool: create state dir: %w", err)
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("vmpool: marshal image builders: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".image-builder-*")
	if err != nil {
		return fmt.Errorf("vmpool: create temp image-builder file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(append(raw, '\n')); err == nil {
		err = tmp.Chmod(0o600)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("vmpool: write image builders: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("vmpool: commit image builders: %w", err)
	}
	return nil
}

func vmpoolAppendImageBuilder(project string, rec vmpoolImageBuilder) error {
	list, err := vmpoolLoadImageBuilders(project)
	if err != nil {
		return err
	}
	list = append(list, rec)
	return vmpoolSaveImageBuilders(project, list)
}

// vmpoolResolveBuilder finds a recorded builder by droplet ID or by name,
// preferring an exact droplet-id match, then falling back to the most
// recently created name match. A ref that resolves to nothing recorded but is
// itself numeric is treated as a bare droplet ID, so a builder created out of
// band (or whose metadata was lost) can still be finalized.
func vmpoolResolveBuilder(project, ref string) (vmpoolImageBuilder, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return vmpoolImageBuilder{}, fmt.Errorf("vmpool: --builder is required")
	}
	list, err := vmpoolLoadImageBuilders(project)
	if err != nil {
		return vmpoolImageBuilder{}, err
	}
	var best vmpoolImageBuilder
	found := false
	for _, b := range list {
		if b.DropletID == ref {
			return b, nil
		}
		if b.Name == ref && (!found || b.CreatedAt.After(best.CreatedAt)) {
			best, found = b, true
		}
	}
	if found {
		return best, nil
	}
	if _, err := strconv.Atoi(ref); err == nil {
		return vmpoolImageBuilder{DropletID: ref, Name: ref}, nil
	}
	return vmpoolImageBuilder{}, fmt.Errorf("vmpool: no recorded image builder matches %q", ref)
}

func vmpoolImageBuildCmd() *cobra.Command {
	var common vmpoolCommonFlags
	var name, size, region, baseImage, tarballURL, goVersion string
	var nodeMajor int
	var sshKeyIDs []string
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Boot an interactive builder droplet for the worker base image",
		Long:  vmpoolImageBuildDoc,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("vmpool: image build: --name is required")
			}
			prov, err := vmpoolNewProvisioner(common.tokenEnv)
			if err != nil {
				return err
			}
			gv := goVersion
			if strings.TrimSpace(gv) == "" {
				gv = vmpoolDetectGoVersion(common.project)
			}
			userData, err := vmpoolBuilderUserData(vmpoolBuilderSpec{
				TarballURL: tarballURL,
				GoVersion:  gv,
				NodeMajor:  nodeMajor,
			})
			if err != nil {
				return err
			}
			ts := vmpoolNow()
			instanceName := fmt.Sprintf("kitsoki-image-builder-%d", ts.Unix())
			inst, err := prov.Create(cmd.Context(), vmpool.CreateParams{
				Name:      instanceName,
				Region:    region,
				Size:      size,
				Image:     baseImage,
				UserData:  userData,
				SSHKeyIDs: sshKeyIDs,
				Tags:      []string{vmpoolBuilderTag},
			})
			if err != nil {
				return fmt.Errorf("vmpool: create image builder droplet: %w", err)
			}
			rec := vmpoolImageBuilder{
				DropletID:    inst.ID,
				Name:         instanceName,
				CreatedAt:    ts,
				SnapshotName: name,
				Region:       region,
				Size:         size,
			}
			if err := vmpoolAppendImageBuilder(common.project, rec); err != nil {
				return err
			}
			return vmpoolPrintBuildResult(cmd.OutOrStdout(), rec, inst)
		},
	}
	addVMPoolCommonFlags(cmd, &common)
	cmd.Flags().StringVar(&name, "name", "", "snapshot name to record for the eventual 'image finalize' (required)")
	cmd.Flags().StringVar(&size, "size", vmpool.DefaultSize, "DO droplet size slug for the builder")
	cmd.Flags().StringVar(&region, "region", vmpool.DefaultRegion, "DO region slug for the builder")
	cmd.Flags().StringVar(&baseImage, "base-image", vmpoolDefaultBaseImage, "distribution image slug the builder boots from")
	cmd.Flags().StringVar(&tarballURL, "kitsoki-tarball-url", "", "optional kitsoki source tarball URL, fetched and built into /usr/local/bin/kitsoki on boot; when omitted, the operator scp's a built binary to /usr/local/bin/kitsoki over SSH before finalize")
	cmd.Flags().StringVar(&goVersion, "go-version", "", "Go toolchain version installed on the builder (default: parsed from --project's go.mod)")
	cmd.Flags().IntVar(&nodeMajor, "node-major", 20, "Node.js major version installed on the builder (for the claude/codex CLIs)")
	cmd.Flags().StringSliceVar(&sshKeyIDs, "ssh-key-id", nil, "DO SSH key id to install on the builder (repeatable; typically the same key(s) retained in the pool Config)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func vmpoolPrintBuildResult(w io.Writer, rec vmpoolImageBuilder, inst vmpool.Instance) error {
	lines := []string{
		fmt.Sprintf("builder droplet created: id=%s name=%s ip=%s", inst.ID, rec.Name, ifEmpty(inst.PublicIP, "<pending>")),
		"",
		"Once the droplet is active, start the login-assist session:",
		fmt.Sprintf("  ssh root@%s", ifEmpty(inst.PublicIP, "<droplet-ip>")),
		"",
		"Checklist to run interactively over that SSH session:",
		"  1. claude login   # authenticate the Claude CLI",
		"  2. codex login    # authenticate the Codex CLI",
		"  3. kitsoki --version   # confirm the kitsoki binary this image will ship",
		"  4. " + vmpoolVerifyScriptPath + "   # confirms both CLIs, kitsoki, and their credential files are present; must print VERIFIED",
		"",
		vmpoolSecurityNote,
		"",
		fmt.Sprintf("Once VERIFIED, finalize with:\n  kitsoki vmpool image finalize --name %s --builder %s", rec.SnapshotName, rec.Name),
	}
	_, err := fmt.Fprintln(w, strings.Join(lines, "\n"))
	return err
}

func vmpoolImageFinalizeCmd() *cobra.Command {
	var common vmpoolCommonFlags
	var name, builderRef string
	var destroyBuilder bool
	cmd := &cobra.Command{
		Use:   "finalize",
		Short: "Snapshot a verified builder droplet into the worker base image",
		Long: "Snapshots the builder droplet named by --builder (droplet ID or the name recorded at\n" +
			"`image build` time) into a new image, after the operator has confirmed " + vmpoolVerifyScriptPath + "\n" +
			"reports VERIFIED over SSH. DigitalOcean permits snapshotting a running droplet.\n\n" + vmpoolSecurityNote,
		RunE: func(cmd *cobra.Command, _ []string) error {
			prov, err := vmpoolNewProvisioner(common.tokenEnv)
			if err != nil {
				return err
			}
			rec, err := vmpoolResolveBuilder(common.project, builderRef)
			if err != nil {
				return err
			}
			snapName := strings.TrimSpace(name)
			if snapName == "" {
				snapName = rec.SnapshotName
			}
			if snapName == "" {
				return fmt.Errorf("vmpool: image finalize: --name is required (no snapshot name recorded for builder %q)", builderRef)
			}
			imageID, err := prov.Snapshot(cmd.Context(), rec.DropletID, snapName)
			if err != nil {
				return fmt.Errorf("vmpool: snapshot builder %s: %w", rec.DropletID, err)
			}
			if destroyBuilder {
				if err := prov.Destroy(cmd.Context(), rec.DropletID); err != nil {
					return fmt.Errorf("vmpool: destroy builder %s after snapshot: %w", rec.DropletID, err)
				}
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				ImageID          string             `json:"image_id"`
				SnapshotName     string             `json:"snapshot_name"`
				Builder          vmpoolImageBuilder `json:"builder"`
				BuilderDestroyed bool               `json:"builder_destroyed"`
			}{ImageID: imageID, SnapshotName: snapName, Builder: rec, BuilderDestroyed: destroyBuilder})
		},
	}
	addVMPoolCommonFlags(cmd, &common)
	cmd.Flags().StringVar(&name, "name", "", "snapshot name (defaults to the name recorded at 'image build' time)")
	cmd.Flags().StringVar(&builderRef, "builder", "", "builder droplet id or name from 'image build' (required)")
	cmd.Flags().BoolVar(&destroyBuilder, "destroy-builder", false, "destroy the builder droplet once the snapshot completes")
	_ = cmd.MarkFlagRequired("builder")
	return cmd
}

func vmpoolImageListCmd() *cobra.Command {
	var common vmpoolCommonFlags
	var cfgFlags vmpoolConfigFlags
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show the configured worker image and any recorded image builders",
		RunE: func(cmd *cobra.Command, _ []string) error {
			builders, err := vmpoolLoadImageBuilders(common.project)
			if err != nil {
				return err
			}
			out := struct {
				Image    string               `json:"image"`
				Note     string               `json:"note"`
				Builders []vmpoolImageBuilder `json:"builders"`
			}{
				Image:    cfgFlags.config().Image,
				Note:     "Image is resolved by snapshot name or numeric ID against the DigitalOcean account (vmpool.Provisioner.ResolveImage) when a worker is acquired; pass --image to bind a different one.",
				Builders: builders,
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "configured image: %s\n", ifEmpty(out.Image, "(none set - pass --image)")); err != nil {
				return err
			}
			for _, b := range builders {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "builder %s (droplet %s) snapshot=%s created=%s\n",
					b.Name, b.DropletID, b.SnapshotName, b.CreatedAt.Format(time.RFC3339)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	addVMPoolCommonFlags(cmd, &common)
	addVMPoolConfigFlags(cmd, &cfgFlags)
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	return cmd
}

// vmpoolDetectGoVersion parses the `go X.Y.Z` directive out of
// <project>/go.mod so the builder installs a matching toolchain. It falls
// back to vmpoolFallbackGoVersion when go.mod is missing or unparsable,
// rather than failing image build over a detail an explicit --go-version can
// always override.
func vmpoolDetectGoVersion(project string) string {
	raw, err := os.ReadFile(filepath.Join(project, "go.mod"))
	if err != nil {
		return vmpoolFallbackGoVersion
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "go ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "go "))
		if len(fields) > 0 && fields[0] != "" {
			return fields[0]
		}
	}
	return vmpoolFallbackGoVersion
}

// vmpoolBuilderSpec configures the builder's cloud-init user data.
type vmpoolBuilderSpec struct {
	TarballURL string
	GoVersion  string
	NodeMajor  int
}

// heredoc markers for the builder cloud-init script. userdata.go's
// GenerateUserData helpers are unexported and specific to the per-job worker
// BootSpec, so the builder script (a different, credential-free shape) is
// composed locally with the same single-quoted-heredoc-per-secret idiom.
const (
	vmpoolBuilderMarkerEnv    = "KITSOKI_VMPOOL_BUILDER_ENV_EOF"
	vmpoolBuilderMarkerVerify = "KITSOKI_VMPOOL_BUILDER_VERIFY_EOF"
)

// vmpoolBuilderUserData renders the builder droplet's cloud-init script. The
// builder carries no worker identity or job secrets — it is an interactive
// box whose only sensitive state (claude/codex credentials) is created later
// by a human logging in over SSH, so nothing here needs the heredoc-safety
// machinery userdata.go applies to per-job tokens and TLS keys beyond
// guarding against the marker itself appearing verbatim in an env value.
func vmpoolBuilderUserData(spec vmpoolBuilderSpec) (string, error) {
	if strings.Contains(spec.TarballURL, "\n") {
		return "", fmt.Errorf("vmpool: kitsoki tarball url must not contain a newline")
	}
	if strings.TrimSpace(spec.GoVersion) == "" {
		return "", fmt.Errorf("vmpool: go version is required")
	}
	nodeMajor := spec.NodeMajor
	if nodeMajor <= 0 {
		nodeMajor = 20
	}

	envLines := []string{
		"KITSOKI_TARBALL_URL=" + spec.TarballURL,
		"GO_VERSION=" + spec.GoVersion,
		fmt.Sprintf("NODE_MAJOR=%d", nodeMajor),
	}
	for _, l := range envLines {
		if l == vmpoolBuilderMarkerEnv {
			return "", fmt.Errorf("vmpool: value collides with heredoc marker %s", vmpoolBuilderMarkerEnv)
		}
	}

	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString("set -euo pipefail\n\n")
	b.WriteString("mkdir -p /etc/kitsoki-image-builder\n")
	b.WriteString("mkdir -p /var/log/kitsoki-image-builder\n\n")
	b.WriteString("log() {\n")
	b.WriteString("  printf '%s %s\\n' \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\" \"$1\" >> /var/log/kitsoki-image-builder/boot.log\n")
	b.WriteString("}\n\n")

	b.WriteString("log 'boot: writing image-builder env'\n")
	fmt.Fprintf(&b, "cat <<'%s' > /etc/kitsoki-image-builder/env\n", vmpoolBuilderMarkerEnv)
	for _, l := range envLines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%s\n", vmpoolBuilderMarkerEnv)
	b.WriteString("chmod 0644 /etc/kitsoki-image-builder/env\n")
	b.WriteString("set -a\n. /etc/kitsoki-image-builder/env\nset +a\n\n")

	b.WriteString("export DEBIAN_FRONTEND=noninteractive\n")
	b.WriteString("log 'boot: apt-get update/install base packages'\n")
	b.WriteString("apt-get update -y\n")
	b.WriteString("apt-get install -y git make curl ca-certificates gnupg build-essential\n\n")

	b.WriteString("log 'boot: installing Node ${NODE_MAJOR}'\n")
	b.WriteString("curl -fsSL \"https://deb.nodesource.com/setup_${NODE_MAJOR}.x\" | bash -\n")
	b.WriteString("apt-get install -y nodejs\n\n")

	b.WriteString("log 'boot: installing Go ${GO_VERSION}'\n")
	b.WriteString("curl -fsSL \"https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz\" -o /tmp/go.tgz\n")
	b.WriteString("rm -rf /usr/local/go\n")
	b.WriteString("tar -C /usr/local -xzf /tmp/go.tgz\n")
	b.WriteString("printf 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin\\n' > /etc/profile.d/kitsoki-go.sh\n")
	b.WriteString("export PATH=$PATH:/usr/local/go/bin\n\n")

	b.WriteString("log 'boot: installing kitsoki binary'\n")
	b.WriteString("if [ -n \"${KITSOKI_TARBALL_URL}\" ]; then\n")
	b.WriteString("  curl -fsSL \"$KITSOKI_TARBALL_URL\" -o /tmp/kitsoki-src.tar.gz\n")
	b.WriteString("  mkdir -p /tmp/kitsoki-src\n")
	b.WriteString("  tar -C /tmp/kitsoki-src --strip-components=1 -xzf /tmp/kitsoki-src.tar.gz\n")
	b.WriteString("  (cd /tmp/kitsoki-src && /usr/local/go/bin/go build -o /usr/local/bin/kitsoki ./cmd/kitsoki)\n")
	b.WriteString("  log 'boot: kitsoki binary built from tarball'\n")
	b.WriteString("else\n")
	b.WriteString("  log 'boot: no kitsoki tarball url given; operator must scp the kitsoki binary to /usr/local/bin/kitsoki over SSH before finalize'\n")
	b.WriteString("fi\n\n")

	b.WriteString("log 'boot: installing claude and codex CLIs'\n")
	b.WriteString("npm install -g @anthropic-ai/claude-code @openai/codex\n\n")

	b.WriteString("log 'boot: writing kitsoki-image-verify'\n")
	fmt.Fprintf(&b, "cat <<'%s' > %s\n", vmpoolBuilderMarkerVerify, vmpoolVerifyScriptPath)
	for _, l := range vmpoolVerifyScriptLines() {
		b.WriteString(l)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%s\n", vmpoolBuilderMarkerVerify)
	fmt.Fprintf(&b, "chmod 0755 %s\n\n", vmpoolVerifyScriptPath)

	b.WriteString("log 'boot: image builder bootstrap complete'\n")

	return b.String(), nil
}

// vmpoolVerifyScriptLines is the body of kitsoki-image-verify, written to the
// builder by cloud-init. It checks, dependency-free (no SSH library in this
// binary's deps), that the kitsoki binary and both agent CLIs are present and
// that their credential artifacts exist, printing VERIFIED only when every
// check passes.
func vmpoolVerifyScriptLines() []string {
	return []string{
		"#!/bin/bash",
		"set -uo pipefail",
		"missing=()",
		`command -v kitsoki >/dev/null 2>&1 || missing+=("kitsoki binary")`,
		`command -v claude >/dev/null 2>&1 || missing+=("claude CLI")`,
		`command -v codex >/dev/null 2>&1 || missing+=("codex CLI")`,
		`[ -f "$HOME/.claude/.credentials.json" ] || missing+=("claude credentials ($HOME/.claude/.credentials.json)")`,
		`[ -f "$HOME/.codex/auth.json" ] || missing+=("codex credentials ($HOME/.codex/auth.json)")`,
		`if [ ${#missing[@]} -eq 0 ]; then`,
		`  echo "VERIFIED"`,
		`  kitsoki --version || true`,
		`  exit 0`,
		`fi`,
		`echo "MISSING:"`,
		`for m in "${missing[@]}"; do echo "  - $m"; done`,
		`exit 1`,
	}
}

func ifEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
