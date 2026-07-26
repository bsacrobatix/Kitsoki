package vmpool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func validImagePointer(generation uint64, imageID string) ImagePointer {
	return ImagePointer{
		Schema:      WorkerImagePointerSchema,
		Environment: "production",
		Generation:  generation,
		SourceSHA:   strings.Repeat("a", 40),
		ImageID:     imageID,
		ImageDigest: "sha256:" + strings.Repeat("b", 64),
		ActivatedAt: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
	}
}

func writeImagePointer(t *testing.T, path string, pointer ImagePointer) {
	t.Helper()
	raw, err := json.Marshal(pointer)
	if err != nil {
		t.Fatal(err)
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func trustedTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func currentOwnerImagePointerLoader(path string) (ImagePointer, error) {
	return loadWorkerImagePointer(path, uint32(os.Getuid()))
}

func TestLoadWorkerImagePointerStrictValidSnapshot(t *testing.T) {
	path := filepath.Join(trustedTempDir(t), "worker-image.json")
	want := validImagePointer(17, "192837465")
	writeImagePointer(t, path, want)

	got, err := currentOwnerImagePointerLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("pointer=%#v, want %#v", got, want)
	}
}

func TestLoadWorkerImagePointerAcceptsSHA256SourceIdentity(t *testing.T) {
	path := filepath.Join(trustedTempDir(t), "worker-image.json")
	want := validImagePointer(18, "192837466")
	want.SourceSHA = strings.Repeat("c", 64)
	writeImagePointer(t, path, want)
	if _, err := currentOwnerImagePointerLoader(path); err != nil {
		t.Fatalf("64-character source SHA rejected: %v", err)
	}
}

func TestLoadWorkerImagePointerRejectsUnsafeFilesystemShapes(t *testing.T) {
	root := trustedTempDir(t)
	validPath := filepath.Join(root, "valid.json")
	writeImagePointer(t, validPath, validImagePointer(1, "100"))

	symlinkPath := filepath.Join(root, "symlink.json")
	if err := os.Symlink(validPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := currentOwnerImagePointerLoader(symlinkPath); err == nil {
		t.Fatal("symlink pointer unexpectedly accepted")
	}

	if err := os.Chmod(validPath, 0o622); err != nil {
		t.Fatal(err)
	}
	if _, err := currentOwnerImagePointerLoader(validPath); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("unsafe mode error=%v", err)
	}

	if _, err := currentOwnerImagePointerLoader(root); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory error=%v", err)
	}
	if _, err := currentOwnerImagePointerLoader("relative.json"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative path error=%v", err)
	}
}

func TestLoadWorkerImagePointerRejectsUnsafeParent(t *testing.T) {
	root := trustedTempDir(t)
	unsafe := filepath.Join(root, "unsafe")
	if err := os.Mkdir(unsafe, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(unsafe, "worker-image.json")
	writeImagePointer(t, path, validImagePointer(1, "100"))
	if err := os.Chmod(unsafe, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unsafe, 0o700) })
	if _, err := currentOwnerImagePointerLoader(path); err == nil || !strings.Contains(err.Error(), "parent") || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("unsafe parent error=%v", err)
	}

	actual := filepath.Join(root, "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	linkedPath := filepath.Join(link, "worker-image.json")
	writeImagePointer(t, filepath.Join(actual, "worker-image.json"), validImagePointer(1, "100"))
	if _, err := currentOwnerImagePointerLoader(linkedPath); err == nil || !strings.Contains(err.Error(), "parent") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink parent error=%v", err)
	}
}

func TestLoadWorkerImagePointerProductionRequiresRootOwner(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test process is root; non-root ownership rejection is covered by the owner-aware loader tests")
	}
	path := filepath.Join(trustedTempDir(t), "worker-image.json")
	writeImagePointer(t, path, validImagePointer(1, "100"))
	if _, err := LoadWorkerImagePointer(path); err == nil || !strings.Contains(err.Error(), "owner uid") {
		t.Fatalf("root ownership error=%v", err)
	}
}

func TestLoadWorkerImagePointerRejectsTruncatedUnknownAndMultipleJSON(t *testing.T) {
	path := filepath.Join(trustedTempDir(t), "worker-image.json")
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "truncated", raw: `{"schema":"kitsoki/worker-image-pointer/v1"`, want: "unexpected EOF"},
		{name: "unknown", raw: `{"schema":"kitsoki/worker-image-pointer/v1","unexpected":true}`, want: "unknown field"},
		{name: "multiple", raw: "{}\n{}", want: "multiple JSON values"},
		{name: "empty", raw: "", want: "size"},
		{name: "oversized", raw: strings.Repeat(" ", maxImagePointerBytes+1), want: "size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := currentOwnerImagePointerLoader(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestLoadWorkerImagePointerRejectsInvalidIdentityFields(t *testing.T) {
	valid := validImagePointer(1, "100")
	cases := []struct {
		name string
		edit func(*ImagePointer)
		want string
	}{
		{name: "schema", edit: func(p *ImagePointer) { p.Schema = "wrong/v1" }, want: "schema"},
		{name: "environment", edit: func(p *ImagePointer) { p.Environment = "Production" }, want: "environment"},
		{name: "generation", edit: func(p *ImagePointer) { p.Generation = 0 }, want: "generation"},
		{name: "sha", edit: func(p *ImagePointer) { p.SourceSHA = "main" }, want: "source_sha"},
		{name: "image id", edit: func(p *ImagePointer) { p.ImageID = "worker-latest" }, want: "image_id"},
		{name: "digest", edit: func(p *ImagePointer) { p.ImageDigest = strings.Repeat("b", 64) }, want: "image_digest"},
		{name: "activated", edit: func(p *ImagePointer) { p.ActivatedAt = time.Time{} }, want: "activated_at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pointer := valid
			tc.edit(&pointer)
			path := filepath.Join(trustedTempDir(t), "worker-image.json")
			writeImagePointer(t, path, pointer)
			_, err := currentOwnerImagePointerLoader(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want substring %q", err, tc.want)
			}
		})
	}
}

type recordingProvisioner struct {
	*stubProvisioner
	mu     sync.Mutex
	images []string
}

func (r *recordingProvisioner) Create(ctx context.Context, params CreateParams) (Instance, error) {
	r.mu.Lock()
	r.images = append(r.images, params.Image)
	r.mu.Unlock()
	return r.stubProvisioner.Create(ctx, params)
}

func TestAcquireResolvesPointerPerWorkerAndPersistsExactBinding(t *testing.T) {
	root := trustedTempDir(t)
	path := filepath.Join(root, "worker-image.json")
	writeImagePointer(t, path, validImagePointer(7, "700"))
	provisioner := &recordingProvisioner{stubProvisioner: &stubProvisioner{}}
	cfg := testConfig()
	cfg.Image = ""
	cfg.ImagePointerPath = path
	cfg.ImagePointerEnvironment = "production"
	pool := newTestPool(root, cfg, provisioner, nil)
	pool.ImagePointerLoader = currentOwnerImagePointerLoader

	first, err := pool.Acquire(context.Background(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	writeImagePointer(t, path, validImagePointer(8, "800"))
	second, err := pool.Acquire(context.Background(), "job-2")
	if err != nil {
		t.Fatal(err)
	}

	if first.Image != "700" || first.ImageGeneration != 7 || second.Image != "800" || second.ImageGeneration != 8 {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if first.ImageDigest == "" || first.ImageSourceSHA == "" || first.ImageEnvironment != "production" {
		t.Fatalf("first pointer identity incomplete: %#v", first)
	}
	state, err := pool.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	durableFirst, ok := state.WorkerByID(first.ID)
	if !ok || durableFirst.Image != "700" || durableFirst.ImageGeneration != 7 {
		t.Fatalf("first durable binding changed after pointer activation: %#v", durableFirst)
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	if len(provisioner.images) != 2 || provisioner.images[0] != "700" || provisioner.images[1] != "800" {
		t.Fatalf("provider images=%v", provisioner.images)
	}
}

func TestAcquireBindsLoadedSnapshotWhenPointerChangesBeforeStoreAndCreate(t *testing.T) {
	root := trustedTempDir(t)
	path := filepath.Join(root, "worker-image.json")
	writeImagePointer(t, path, validImagePointer(11, "1100"))
	provisioner := &recordingProvisioner{stubProvisioner: &stubProvisioner{}}
	cfg := testConfig()
	cfg.Image = ""
	cfg.ImagePointerPath = path
	cfg.ImagePointerEnvironment = "production"
	pool := newTestPool(root, cfg, provisioner, nil)
	pool.ImagePointerLoader = func(path string) (ImagePointer, error) {
		loaded, err := currentOwnerImagePointerLoader(path)
		if err != nil {
			return ImagePointer{}, err
		}
		// Simulate the deployment controller atomically activating the next
		// generation after this Acquire opened its snapshot but before the
		// durable record and provider request are made.
		writeImagePointer(t, path, validImagePointer(12, "1200"))
		return loaded, nil
	}

	worker, err := pool.Acquire(context.Background(), "job-boundary")
	if err != nil {
		t.Fatal(err)
	}
	if worker.Image != "1100" || worker.ImageGeneration != 11 {
		t.Fatalf("worker crossed pointer generations: %#v", worker)
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	if len(provisioner.images) != 1 || provisioner.images[0] != "1100" {
		t.Fatalf("provider image=%v, want loaded generation image 1100", provisioner.images)
	}
}

func TestAcquirePersistsFullPointerIdentityBeforeProviderCreate(t *testing.T) {
	root := trustedTempDir(t)
	path := filepath.Join(root, "worker-image.json")
	pointer := validImagePointer(21, "2100")
	writeImagePointer(t, path, pointer)
	sawExactRecord := false
	provisioner := &stubProvisioner{create: func(_ context.Context, params CreateParams) (Instance, error) {
		state, err := (Store{ProjectRoot: root}).Load()
		if err != nil {
			t.Fatal(err)
		}
		worker, ok := state.WorkerByID(workerID("job-evidence"))
		sawExactRecord = ok &&
			worker.Status == StatusCreating &&
			worker.Image == pointer.ImageID &&
			worker.ImageGeneration == pointer.Generation &&
			worker.ImageDigest == pointer.ImageDigest &&
			worker.ImageSourceSHA == pointer.SourceSHA &&
			worker.ImageEnvironment == pointer.Environment &&
			params.Image == pointer.ImageID
		return Instance{ID: "instance-evidence", Status: "new"}, nil
	}}
	cfg := testConfig()
	cfg.Image = ""
	cfg.ImagePointerPath = path
	cfg.ImagePointerEnvironment = "production"
	pool := newTestPool(root, cfg, provisioner, nil)
	pool.ImagePointerLoader = currentOwnerImagePointerLoader
	if _, err := pool.Acquire(context.Background(), "job-evidence"); err != nil {
		t.Fatal(err)
	}
	if !sawExactRecord {
		t.Fatal("full pointer identity was not durable before provider Create")
	}
}

func TestAcquirePointerFailureHasNoFallbackAndNoDurableWorker(t *testing.T) {
	root := trustedTempDir(t)
	cfg := testConfig()
	cfg.Image = ""
	cfg.ImagePointerPath = filepath.Join(root, "missing.json")
	cfg.ImagePointerEnvironment = "production"
	createCalls := 0
	provisioner := &stubProvisioner{create: func(context.Context, CreateParams) (Instance, error) {
		createCalls++
		return Instance{}, nil
	}}
	pool := newTestPool(root, cfg, provisioner, nil)
	pool.ImagePointerLoader = currentOwnerImagePointerLoader

	if _, err := pool.Acquire(context.Background(), "job-1"); err == nil || !strings.Contains(err.Error(), "resolve worker image") {
		t.Fatalf("error=%v", err)
	}
	if createCalls != 0 {
		t.Fatalf("provider Create called %d times", createCalls)
	}
	state, err := pool.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Workers) != 0 {
		t.Fatalf("failed pointer load created durable workers: %#v", state.Workers)
	}
}

func TestAcquirePointerRejectsEnvironmentMismatchAndAmbiguousConfig(t *testing.T) {
	root := trustedTempDir(t)
	path := filepath.Join(root, "worker-image.json")
	writeImagePointer(t, path, validImagePointer(1, "100"))

	cfg := testConfig()
	cfg.Image = ""
	cfg.ImagePointerPath = path
	cfg.ImagePointerEnvironment = "staging"
	pool := newTestPool(root, cfg, &stubProvisioner{}, nil)
	pool.ImagePointerLoader = currentOwnerImagePointerLoader
	if _, err := pool.Acquire(context.Background(), "job-1"); err == nil || !strings.Contains(err.Error(), "environment") {
		t.Fatalf("environment mismatch error=%v", err)
	}

	cfg.Image = "static"
	pool = newTestPool(root, cfg, &stubProvisioner{}, nil)
	if _, err := pool.Acquire(context.Background(), "job-2"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("ambiguous config error=%v", err)
	}
}

func TestAcquirePreservesStaticImageOutsidePointerMode(t *testing.T) {
	provisioner := &recordingProvisioner{stubProvisioner: &stubProvisioner{}}
	pool := newTestPool(t.TempDir(), testConfig(), provisioner, nil)
	worker, err := pool.Acquire(context.Background(), "job-static")
	if err != nil {
		t.Fatal(err)
	}
	if worker.Image != "test-image" || worker.ImageGeneration != 0 || worker.ImageDigest != "" {
		t.Fatalf("static worker=%#v", worker)
	}
}
