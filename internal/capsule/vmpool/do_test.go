package vmpool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// newTestDO spins up an httptest server and returns a DO wired to it, plus
// the mux to register handlers on. No real network call ever leaves the
// process.
func newTestDO(t *testing.T) (*DO, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	d := NewDO("test-token")
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	d.Client.BaseURL = base
	d.PollInterval = 5 * time.Millisecond
	return d, mux
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprint(w, body)
}

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return out
}

func TestDOCreateMapsParamsWithNumericImage(t *testing.T) {
	d, mux := newTestDO(t)

	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		body := decodeBody(t, r)
		if body["region"] != "sgp1" {
			t.Errorf("region: got %v", body["region"])
		}
		if body["size"] != "s-4vcpu-16gb" {
			t.Errorf("size: got %v", body["size"])
		}
		if body["image"] != float64(12345) {
			t.Errorf("image: got %v (%T)", body["image"], body["image"])
		}
		if body["user_data"] != "#cloud-config\nfoo" {
			t.Errorf("user_data: got %v", body["user_data"])
		}
		if body["vpc_uuid"] != "vpc-1" {
			t.Errorf("vpc_uuid: got %v", body["vpc_uuid"])
		}
		tags, _ := body["tags"].([]any)
		if len(tags) != 1 || tags[0] != "kitsoki-worker" {
			t.Errorf("tags: got %v", body["tags"])
		}
		keys, _ := body["ssh_keys"].([]any)
		if len(keys) != 1 || keys[0] != float64(999) {
			t.Errorf("ssh_keys: got %v", body["ssh_keys"])
		}
		writeJSON(t, w, http.StatusCreated, `{"droplet": {
			"id": 42, "name": "kitsoki-worker-1", "status": "new",
			"networks": {"v4": [
				{"ip_address": "1.2.3.4", "type": "public"},
				{"ip_address": "10.0.0.5", "type": "private"}
			]},
			"tags": ["kitsoki-worker"],
			"created_at": "2026-01-02T03:04:05Z"
		}}`)
	})

	inst, err := d.Create(context.Background(), CreateParams{
		Name: "kitsoki-worker-1", Region: "sgp1", Size: "s-4vcpu-16gb",
		Image: "12345", VPCUUID: "vpc-1", UserData: "#cloud-config\nfoo",
		SSHKeyIDs: []string{"999"}, Tags: []string{"kitsoki-worker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.ID != "42" || inst.Name != "kitsoki-worker-1" || inst.Status != "new" {
		t.Fatalf("unexpected instance: %#v", inst)
	}
	if inst.PublicIP != "1.2.3.4" || inst.PrivateIP != "10.0.0.5" {
		t.Fatalf("unexpected IPs: %#v", inst)
	}
	if !inst.CreatedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("unexpected CreatedAt: %v", inst.CreatedAt)
	}
}

func TestDOCreateRetriesWithoutTagsOnPermissionError(t *testing.T) {
	d, mux := newTestDO(t)

	var attempts int32
	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt == 1 {
			tags, _ := body["tags"].([]any)
			if len(tags) == 0 {
				t.Fatalf("expected first attempt to include tags, got %v", body["tags"])
			}
			writeJSON(t, w, http.StatusForbidden, `{"id":"Forbidden","message":"unable to create droplet: you do not have permission tag:create"}`)
			return
		}
		if tags := body["tags"]; tags != nil {
			if arr, ok := tags.([]any); ok && len(arr) != 0 {
				t.Fatalf("expected retry without tags, got %v", tags)
			}
		}
		writeJSON(t, w, http.StatusCreated, `{"droplet": {"id": 7, "name": "w", "status": "new"}}`)
	})

	inst, err := d.Create(context.Background(), CreateParams{
		Name: "w", Region: "sgp1", Size: "s-4vcpu-16gb", Image: "12345",
		Tags: []string{"kitsoki-worker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.ID != "7" {
		t.Fatalf("expected instance from retried create, got %#v", inst)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("expected exactly 2 create attempts, got %d", got)
	}
}

func TestDOCreateResolvesSnapshotNameToImageID(t *testing.T) {
	d, mux := newTestDO(t)

	mux.HandleFunc("/v2/snapshots", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("resource_type"); got != "droplet" {
			t.Fatalf("expected resource_type=droplet, got %q", got)
		}
		writeJSON(t, w, http.StatusOK, `{"snapshots": [
			{"id": "555", "name": "other-snapshot"},
			{"id": "777", "name": "base-image"}
		]}`)
	})
	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		if body["image"] != float64(777) {
			t.Fatalf("expected resolved snapshot image id 777, got %v", body["image"])
		}
		writeJSON(t, w, http.StatusCreated, `{"droplet": {"id": 1, "name": "w", "status": "new"}}`)
	})

	if _, err := d.Create(context.Background(), CreateParams{
		Name: "w", Region: "sgp1", Size: "s-4vcpu-16gb", Image: "base-image",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDOCreateFallsBackToSlugWhenSnapshotNameNotFound(t *testing.T) {
	d, mux := newTestDO(t)

	mux.HandleFunc("/v2/snapshots", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"snapshots": []}`)
	})
	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		if body["image"] != "ubuntu-22-04-x64" {
			t.Fatalf("expected slug pass-through, got %v", body["image"])
		}
		writeJSON(t, w, http.StatusCreated, `{"droplet": {"id": 1, "name": "w", "status": "new"}}`)
	})

	if _, err := d.Create(context.Background(), CreateParams{
		Name: "w", Region: "sgp1", Size: "s-4vcpu-16gb", Image: "ubuntu-22-04-x64",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDOGetFound(t *testing.T) {
	d, mux := newTestDO(t)
	mux.HandleFunc("/v2/droplets/42", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		writeJSON(t, w, http.StatusOK, `{"droplet": {"id": 42, "name": "w", "status": "active"}}`)
	})

	inst, found, err := d.Get(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if !found || inst.ID != "42" || inst.Status != "active" {
		t.Fatalf("unexpected result: found=%v inst=%#v", found, inst)
	}
}

func TestDOGetNotFound(t *testing.T) {
	d, mux := newTestDO(t)
	mux.HandleFunc("/v2/droplets/404", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, `{"id":"not_found","message":"The resource you were accessing could not be found."}`)
	})

	_, found, err := d.Get(context.Background(), "404")
	if err != nil {
		t.Fatalf("expected nil error on 404, got %v", err)
	}
	if found {
		t.Fatal("expected found=false for unknown droplet")
	}
}

func TestDODestroyIdempotent(t *testing.T) {
	d, mux := newTestDO(t)
	mux.HandleFunc("/v2/droplets/404", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("expected DELETE, got %s", r.Method)
		}
		writeJSON(t, w, http.StatusNotFound, `{"id":"not_found","message":"The resource you were accessing could not be found."}`)
	})
	mux.HandleFunc("/v2/droplets/7", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	if err := d.Destroy(context.Background(), "404"); err != nil {
		t.Fatalf("expected 404 destroy to be idempotent, got %v", err)
	}
	if err := d.Destroy(context.Background(), "7"); err != nil {
		t.Fatalf("expected successful destroy, got %v", err)
	}
}

func TestDOListByTagPaginates(t *testing.T) {
	d, mux := newTestDO(t)
	nextPageURL := d.Client.BaseURL.String() + "/v2/droplets?tag_name=kitsoki-worker&page=2&per_page=200"
	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		if tag := r.URL.Query().Get("tag_name"); tag != "kitsoki-worker" {
			t.Fatalf("expected tag_name=kitsoki-worker, got %q", tag)
		}
		switch r.URL.Query().Get("page") {
		case "", "1":
			writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{
				"droplets": [{"id": 1, "name": "a", "status": "active"}],
				"links": {"pages": {"next": %q}}
			}`, nextPageURL))
		case "2":
			writeJSON(t, w, http.StatusOK, `{
				"droplets": [{"id": 2, "name": "b", "status": "active"}],
				"links": {"pages": {"prev": "ignored"}}
			}`)
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
		}
	})

	instances, err := d.ListByTag(context.Background(), "kitsoki-worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 2 || instances[0].ID != "1" || instances[1].ID != "2" {
		t.Fatalf("expected 2 instances across pages, got %#v", instances)
	}
}

func TestDOSnapshotPollsUntilCompletedThenResolvesID(t *testing.T) {
	d, mux := newTestDO(t)

	var actionCalls int32
	mux.HandleFunc("/v2/droplets/42/actions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		body := decodeBody(t, r)
		if body["type"] != "snapshot" || body["name"] != "base-image" {
			t.Fatalf("unexpected snapshot action request: %v", body)
		}
		writeJSON(t, w, http.StatusCreated, `{"action": {"id": 999, "status": "in-progress"}}`)
	})
	mux.HandleFunc("/v2/actions/999", func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&actionCalls, 1)
		if call == 1 {
			writeJSON(t, w, http.StatusOK, `{"action": {"id": 999, "status": "in-progress"}}`)
			return
		}
		writeJSON(t, w, http.StatusOK, `{"action": {"id": 999, "status": "completed"}}`)
	})
	mux.HandleFunc("/v2/droplets/42/snapshots", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"snapshots": [
			{"id": 321, "name": "unrelated"},
			{"id": 654, "name": "base-image"}
		]}`)
	})

	imageID, err := d.Snapshot(context.Background(), "42", "base-image")
	if err != nil {
		t.Fatal(err)
	}
	if imageID != "654" {
		t.Fatalf("expected image ID 654, got %q", imageID)
	}
	if got := atomic.LoadInt32(&actionCalls); got < 2 {
		t.Fatalf("expected at least 2 polls before completion, got %d", got)
	}
}

func TestDOSnapshotRespectsContextCancellation(t *testing.T) {
	d, mux := newTestDO(t)
	mux.HandleFunc("/v2/droplets/42/actions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusCreated, `{"action": {"id": 999, "status": "in-progress"}}`)
	})
	mux.HandleFunc("/v2/actions/999", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"action": {"id": 999, "status": "in-progress"}}`)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := d.Snapshot(ctx, "42", "base-image")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestDOResolveImage(t *testing.T) {
	d, mux := newTestDO(t)
	mux.HandleFunc("/v2/snapshots", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"snapshots": [{"id": "654", "name": "base-image"}]}`)
	})

	numeric, err := d.ResolveImage(context.Background(), "12345")
	if err != nil {
		t.Fatal(err)
	}
	if numeric != "12345" {
		t.Fatalf("expected numeric passthrough, got %q", numeric)
	}

	resolved, err := d.ResolveImage(context.Background(), "base-image")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "654" {
		t.Fatalf("expected resolved snapshot ID, got %q", resolved)
	}

	slug, err := d.ResolveImage(context.Background(), "ubuntu-22-04-x64")
	if err != nil {
		t.Fatal(err)
	}
	if slug != "ubuntu-22-04-x64" {
		t.Fatalf("expected slug passthrough, got %q", slug)
	}
}
