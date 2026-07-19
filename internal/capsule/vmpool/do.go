package vmpool

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/digitalocean/godo"
)

// defaultSnapshotPollInterval bounds how often Snapshot polls the DO action
// status while waiting for a snapshot to complete.
const defaultSnapshotPollInterval = 5 * time.Second

// DO is the DigitalOcean Provisioner, ported from rumbledunk's
// vmworkerpool.DOOrchestrator. It is safe for concurrent use: all state lives
// in the godo client, which is itself concurrency-safe.
type DO struct {
	// Client is the underlying godo client. It is exported so tests can
	// redirect Client.BaseURL at an httptest server; production callers
	// should otherwise leave it as constructed by NewDO.
	Client *godo.Client

	// PollInterval bounds how often Snapshot polls the DO action status. A
	// zero value uses defaultSnapshotPollInterval; tests set this small to
	// avoid slow polling loops.
	PollInterval time.Duration
}

var _ Provisioner = (*DO)(nil)

// NewDO builds a DigitalOcean provisioner authenticated with token.
func NewDO(token string) *DO {
	return &DO{Client: godo.NewFromToken(token)}
}

func (d *DO) pollInterval() time.Duration {
	if d.PollInterval > 0 {
		return d.PollInterval
	}
	return defaultSnapshotPollInterval
}

// Create provisions a droplet from params. Image resolution tries, in order:
// a numeric ID, an exact snapshot-name match, then falls through to a
// distribution slug. If the token lacks tag:create permission, the create is
// retried once without tags (ported from rumbledunk's graceful degradation).
func (d *DO) Create(ctx context.Context, params CreateParams) (Instance, error) {
	image, err := d.dropletImage(ctx, params.Image)
	if err != nil {
		return Instance{}, fmt.Errorf("vmpool: resolve image %q: %w", params.Image, err)
	}

	req := &godo.DropletCreateRequest{
		Name:     params.Name,
		Region:   params.Region,
		Size:     params.Size,
		Image:    image,
		VPCUUID:  params.VPCUUID,
		UserData: params.UserData,
		Tags:     append([]string(nil), params.Tags...),
	}
	for _, raw := range params.SSHKeyIDs {
		id, err := strconv.Atoi(raw)
		if err != nil {
			return Instance{}, fmt.Errorf("vmpool: ssh key id %q must be numeric: %w", raw, err)
		}
		req.SSHKeys = append(req.SSHKeys, godo.DropletCreateSSHKey{ID: id})
	}

	droplet, _, err := d.Client.Droplets.Create(ctx, req)
	if err != nil && len(req.Tags) > 0 && strings.Contains(err.Error(), "permission tag:create") {
		req.Tags = nil
		droplet, _, err = d.Client.Droplets.Create(ctx, req)
	}
	if err != nil {
		return Instance{}, fmt.Errorf("vmpool: create droplet %q: %w", params.Name, err)
	}
	return instanceFromDroplet(droplet), nil
}

// Get reports found=false, nil error for a droplet DO no longer knows about.
func (d *DO) Get(ctx context.Context, instanceID string) (Instance, bool, error) {
	id, err := strconv.Atoi(instanceID)
	if err != nil {
		return Instance{}, false, fmt.Errorf("vmpool: instance id %q must be numeric: %w", instanceID, err)
	}
	droplet, resp, err := d.Client.Droplets.Get(ctx, id)
	if err != nil {
		if isNotFound(resp, err) {
			return Instance{}, false, nil
		}
		return Instance{}, false, fmt.Errorf("vmpool: get droplet %s: %w", instanceID, err)
	}
	return instanceFromDroplet(droplet), true, nil
}

// Destroy is idempotent: a 404 from DO means the droplet is already gone.
func (d *DO) Destroy(ctx context.Context, instanceID string) error {
	id, err := strconv.Atoi(instanceID)
	if err != nil {
		return fmt.Errorf("vmpool: instance id %q must be numeric: %w", instanceID, err)
	}
	resp, err := d.Client.Droplets.Delete(ctx, id)
	if err != nil && !isNotFound(resp, err) {
		return fmt.Errorf("vmpool: destroy droplet %s: %w", instanceID, err)
	}
	return nil
}

// ListByTag walks every page of DO's tag-filtered droplet listing.
func (d *DO) ListByTag(ctx context.Context, tag string) ([]Instance, error) {
	var out []Instance
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		droplets, resp, err := d.Client.Droplets.ListByTag(ctx, tag, opt)
		if err != nil {
			return nil, fmt.Errorf("vmpool: list droplets by tag %q: %w", tag, err)
		}
		for i := range droplets {
			out = append(out, instanceFromDroplet(&droplets[i]))
		}
		next, ok, err := nextPage(resp)
		if err != nil {
			return nil, fmt.Errorf("vmpool: paginate droplets by tag %q: %w", tag, err)
		}
		if !ok {
			break
		}
		opt.Page = next
	}
	return out, nil
}

// Snapshot requests a droplet snapshot, polls the resulting action until it
// completes (or errors, or ctx is done), then resolves the new image ID by
// name against the droplet's snapshot list.
func (d *DO) Snapshot(ctx context.Context, instanceID, name string) (string, error) {
	id, err := strconv.Atoi(instanceID)
	if err != nil {
		return "", fmt.Errorf("vmpool: instance id %q must be numeric: %w", instanceID, err)
	}
	action, _, err := d.Client.DropletActions.Snapshot(ctx, id, name)
	if err != nil {
		return "", fmt.Errorf("vmpool: snapshot droplet %s: %w", instanceID, err)
	}

	interval := d.pollInterval()
	for {
		got, _, err := d.Client.Actions.Get(ctx, action.ID)
		if err != nil {
			return "", fmt.Errorf("vmpool: poll snapshot action %d: %w", action.ID, err)
		}
		switch got.Status {
		case godo.ActionCompleted:
			return d.findSnapshotID(ctx, id, name)
		case "errored":
			return "", fmt.Errorf("vmpool: snapshot action %d for droplet %s errored", action.ID, instanceID)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
	}
}

// findSnapshotID walks the droplet's snapshot list looking for an exact name
// match, returning its image ID as a string.
func (d *DO) findSnapshotID(ctx context.Context, dropletID int, name string) (string, error) {
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		snaps, resp, err := d.Client.Droplets.Snapshots(ctx, dropletID, opt)
		if err != nil {
			return "", fmt.Errorf("vmpool: list snapshots for droplet %d: %w", dropletID, err)
		}
		for _, snap := range snaps {
			if snap.Name == name {
				return strconv.Itoa(snap.ID), nil
			}
		}
		next, ok, err := nextPage(resp)
		if err != nil {
			return "", fmt.Errorf("vmpool: paginate snapshots for droplet %d: %w", dropletID, err)
		}
		if !ok {
			break
		}
		opt.Page = next
	}
	return "", fmt.Errorf("vmpool: snapshot %q for droplet %d completed but was not found", name, dropletID)
}

// ResolveImage turns a snapshot name into its numeric ID. A numeric ref is
// returned unchanged; a ref that doesn't match an account snapshot passes
// through unchanged too, since it may be a distribution slug.
func (d *DO) ResolveImage(ctx context.Context, ref string) (string, error) {
	if _, err := strconv.Atoi(ref); err == nil {
		return ref, nil
	}
	id, found, err := d.resolveSnapshotByName(ctx, ref)
	if err != nil {
		return "", err
	}
	if found {
		return strconv.Itoa(id), nil
	}
	return ref, nil
}

// dropletImage resolves a CreateParams.Image ref into the godo image
// selector used on the create request.
func (d *DO) dropletImage(ctx context.Context, ref string) (godo.DropletCreateImage, error) {
	if id, err := strconv.Atoi(ref); err == nil {
		return godo.DropletCreateImage{ID: id}, nil
	}
	id, found, err := d.resolveSnapshotByName(ctx, ref)
	if err != nil {
		return godo.DropletCreateImage{}, err
	}
	if found {
		return godo.DropletCreateImage{ID: id}, nil
	}
	return godo.DropletCreateImage{Slug: ref}, nil
}

// resolveSnapshotByName searches the account's droplet snapshots for an
// exact name match, paginating through the full list.
func (d *DO) resolveSnapshotByName(ctx context.Context, name string) (int, bool, error) {
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		snaps, resp, err := d.Client.Snapshots.ListDroplet(ctx, opt)
		if err != nil {
			return 0, false, fmt.Errorf("vmpool: list snapshots: %w", err)
		}
		for _, snap := range snaps {
			if snap.Name != name {
				continue
			}
			id, err := strconv.Atoi(snap.ID)
			if err != nil {
				return 0, false, fmt.Errorf("vmpool: snapshot %q has non-numeric id %q: %w", name, snap.ID, err)
			}
			return id, true, nil
		}
		next, ok, err := nextPage(resp)
		if err != nil {
			return 0, false, fmt.Errorf("vmpool: paginate snapshots: %w", err)
		}
		if !ok {
			break
		}
		opt.Page = next
	}
	return 0, false, nil
}

// nextPage reports the next page number to request, and whether one exists,
// from a godo response's pagination links.
func nextPage(resp *godo.Response) (int, bool, error) {
	if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
		return 0, false, nil
	}
	page, err := resp.Links.CurrentPage()
	if err != nil {
		return 0, false, err
	}
	return page + 1, true, nil
}

// isNotFound reports whether err represents a 404 from the DO API.
func isNotFound(resp *godo.Response, err error) bool {
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return true
	}
	var errResp *godo.ErrorResponse
	if errors.As(err, &errResp) && errResp.Response != nil {
		return errResp.Response.StatusCode == http.StatusNotFound
	}
	return false
}

// instanceFromDroplet maps a godo Droplet onto the provider-neutral Instance
// shape, extracting public/private IPv4 addresses from its networks.
func instanceFromDroplet(droplet *godo.Droplet) Instance {
	publicIP, _ := droplet.PublicIPv4()
	privateIP, _ := droplet.PrivateIPv4()
	createdAt, _ := time.Parse(time.RFC3339, droplet.Created)
	return Instance{
		ID:        strconv.Itoa(droplet.ID),
		Name:      droplet.Name,
		Status:    droplet.Status,
		PublicIP:  publicIP,
		PrivateIP: privateIP,
		CreatedAt: createdAt,
		Tags:      droplet.Tags,
	}
}
