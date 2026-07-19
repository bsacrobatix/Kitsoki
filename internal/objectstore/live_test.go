package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveSpacesSmoke exercises the real DigitalOcean Spaces bucket. It is
// opt-in only (never runs in automated gates): set KITSOKI_OBJECTSTORE_LIVE=1
// plus the credential env vars. Override the bucket with
// KITSOKI_OBJECTSTORE_LIVE_URL; key/secret env names with
// KITSOKI_OBJECTSTORE_LIVE_KEY_ENV / _SECRET_ENV.
//
//	KITSOKI_OBJECTSTORE_LIVE=1 go test ./internal/objectstore -run TestLiveSpacesSmoke -v
func TestLiveSpacesSmoke(t *testing.T) {
	if os.Getenv("KITSOKI_OBJECTSTORE_LIVE") != "1" {
		t.Skip("live smoke disabled; set KITSOKI_OBJECTSTORE_LIVE=1 to run against the real bucket")
	}
	bucketURL := os.Getenv("KITSOKI_OBJECTSTORE_LIVE_URL")
	if bucketURL == "" {
		bucketURL = "https://kitsoki-test.sgp1.digitaloceanspaces.com"
	}
	keyEnv := os.Getenv("KITSOKI_OBJECTSTORE_LIVE_KEY_ENV")
	if keyEnv == "" {
		keyEnv = "DO_KITSOKI_TEST_API_KEY"
	}
	secretEnv := os.Getenv("KITSOKI_OBJECTSTORE_LIVE_SECRET_ENV")
	if secretEnv == "" {
		secretEnv = "DO_KITSOKI_TEST_API_SECRET"
	}
	cfg, err := ParseBucketURL(bucketURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.KeyEnv, cfg.SecretEnv = keyEnv, secretEnv
	store, err := NewSpaces(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	key := fmt.Sprintf("smoke/%d/hello.txt", time.Now().UnixNano())
	body := "kitsoki objectstore live smoke"

	if _, err := store.Put(ctx, key, strings.NewReader(body), int64(len(body)), PutOptions{ContentType: "text/plain"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Delete(context.Background(), key); err != nil {
			t.Errorf("cleanup Delete: %v", err)
		}
	})

	meta, err := store.Head(ctx, key)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if meta.Size != int64(len(body)) {
		t.Fatalf("Head size = %d, want %d", meta.Size, len(body))
	}

	rc, _, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(data) != body {
		t.Fatalf("Get body = %q (err %v), want %q", data, err, body)
	}

	listed, err := store.List(ctx, "smoke/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, item := range listed {
		if item.Key == key {
			found = true
		}
	}
	if !found {
		t.Fatalf("List(smoke/) did not include %s (%d items)", key, len(listed))
	}

	link, err := store.Presign(ctx, "GET", key, 5*time.Minute)
	if err != nil {
		t.Fatalf("Presign: %v", err)
	}
	resp, err := http.Get(link)
	if err != nil {
		t.Fatalf("fetch presigned: %v", err)
	}
	defer resp.Body.Close()
	presigned, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(presigned) != body {
		t.Fatalf("presigned GET: status %s body %q", resp.Status, presigned)
	}

	if _, err := store.Head(ctx, key+".missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Head missing: got %v, want ErrNotFound", err)
	}
}
