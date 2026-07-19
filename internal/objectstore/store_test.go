package objectstore

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStoreContract runs the same behavioral suite against the in-memory Fake
// and the Spaces client talking to a minimal in-process S3 server that
// independently re-verifies every request signature.
func TestStoreContract(t *testing.T) {
	t.Run("fake", func(t *testing.T) {
		testStoreContract(t, NewFake())
	})
	t.Run("spaces", func(t *testing.T) {
		testStoreContract(t, newTestSpaces(t))
	})
}

func testStoreContract(t *testing.T, store Store) {
	ctx := context.Background()

	t.Run("head and get missing", func(t *testing.T) {
		if _, err := store.Head(ctx, "sources/none/meta.json"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Head missing: got %v, want ErrNotFound", err)
		}
		if _, _, err := store.Get(ctx, "sources/none/meta.json"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get missing: got %v, want ErrNotFound", err)
		}
	})

	t.Run("put get roundtrip", func(t *testing.T) {
		body := "frozen bundle bytes"
		meta, err := store.Put(ctx, "sources/abc123/bundle.git", strings.NewReader(body), int64(len(body)), PutOptions{ContentType: "application/vnd.git.bundle"})
		if err != nil {
			t.Fatal(err)
		}
		if meta.Size != int64(len(body)) {
			t.Fatalf("put meta size = %d, want %d", meta.Size, len(body))
		}
		rc, got, err := store.Get(ctx, "sources/abc123/bundle.git")
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != body {
			t.Fatalf("get body = %q, want %q", data, body)
		}
		if got.Size != int64(len(body)) || got.ContentType != "application/vnd.git.bundle" {
			t.Fatalf("get meta = %+v", got)
		}
		head, err := store.Head(ctx, "sources/abc123/bundle.git")
		if err != nil {
			t.Fatal(err)
		}
		if head.Size != int64(len(body)) {
			t.Fatalf("head size = %d, want %d", head.Size, len(body))
		}
	})

	t.Run("put size mismatch rejected", func(t *testing.T) {
		_, err := store.Put(ctx, "runs/j1/short.txt", strings.NewReader("abc"), 5, PutOptions{})
		if err == nil {
			t.Fatal("expected error for body shorter than declared size")
		}
	})

	t.Run("list by prefix with pagination", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			key := fmt.Sprintf("runs/job-1/artifacts/a%d.txt", i)
			if _, err := store.Put(ctx, key, strings.NewReader("x"), 1, PutOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.Put(ctx, "runs/job-2/trace.jsonl", strings.NewReader("{}"), 2, PutOptions{}); err != nil {
			t.Fatal(err)
		}
		got, err := store.List(ctx, "runs/job-1/")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 5 {
			t.Fatalf("list returned %d keys, want 5: %+v", len(got), got)
		}
		if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Key < got[j].Key }) {
			t.Fatalf("list not sorted: %+v", got)
		}
	})

	t.Run("delete is idempotent", func(t *testing.T) {
		if _, err := store.Put(ctx, "runs/job-3/x", strings.NewReader("x"), 1, PutOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := store.Delete(ctx, "runs/job-3/x"); err != nil {
			t.Fatal(err)
		}
		if err := store.Delete(ctx, "runs/job-3/x"); err != nil {
			t.Fatalf("second delete: %v", err)
		}
		if _, err := store.Head(ctx, "runs/job-3/x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("head after delete: %v", err)
		}
	})

	t.Run("invalid keys rejected", func(t *testing.T) {
		for _, key := range []string{"", "/abs", "a//b", "a/../b"} {
			if _, err := store.Put(ctx, key, strings.NewReader(""), 0, PutOptions{}); err == nil {
				t.Errorf("Put accepted invalid key %q", key)
			}
		}
	})

	t.Run("presign shape", func(t *testing.T) {
		link, err := store.Presign(ctx, "GET", "runs/job-1/artifacts/a0.txt", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if link == "" {
			t.Fatal("empty presigned URL")
		}
		if _, err := store.Presign(ctx, "DELETE", "runs/job-1/artifacts/a0.txt", time.Hour); err == nil {
			t.Fatal("presign DELETE should be rejected")
		}
	})
}

const (
	testKeyID  = "TESTKEYID"
	testSecret = "test-secret-value"
)

// newTestSpaces starts a minimal S3 server and returns a Spaces client wired
// to it through a host-agnostic dialer, so virtual-hosted request URLs
// (bucket.endpoint) resolve without DNS.
func newTestSpaces(t *testing.T) *Spaces {
	t.Helper()
	backend := &miniS3{t: t, objects: map[string]miniObject{}, pageSize: 2}
	server := httptest.NewServer(backend)
	t.Cleanup(server.Close)

	t.Setenv("TEST_SPACES_KEY", testKeyID)
	t.Setenv("TEST_SPACES_SECRET", testSecret)
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("tcp", server.Listener.Addr().String())
			},
		},
	}
	spaces, err := NewSpaces(SpacesConfig{
		Endpoint:  "http://sgp1.spaces.test",
		Bucket:    "kitsoki-test",
		Region:    "sgp1",
		KeyEnv:    "TEST_SPACES_KEY",
		SecretEnv: "TEST_SPACES_SECRET",
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	return spaces
}

type miniObject struct {
	data        []byte
	contentType string
	modified    time.Time
}

// miniS3 implements just enough of the S3 REST dialect for the contract
// suite: virtual-hosted object CRUD plus ListObjectsV2 with continuation
// tokens. Every request's SigV4 signature is re-derived with the shared
// signer and must match, which pins the client's wire encoding to its
// canonical form.
type miniS3 struct {
	t        *testing.T
	mu       sync.Mutex
	objects  map[string]miniObject
	pageSize int
}

func (m *miniS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := m.verifySignature(r); err != nil {
		m.t.Errorf("signature verification failed: %v", err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if !strings.HasPrefix(r.Host, "kitsoki-test.") {
		http.Error(w, "not virtual-hosted", http.StatusBadRequest)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/")
	if key == "" && r.Method == http.MethodGet {
		m.list(w, r)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.ContentLength >= 0 && int64(len(data)) != r.ContentLength {
			http.Error(w, "length mismatch", http.StatusBadRequest)
			return
		}
		m.objects[key] = miniObject{data: data, contentType: r.Header.Get("Content-Type"), modified: time.Now().UTC()}
		w.Header().Set("ETag", `"mini-etag"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		obj, ok := m.objects[key]
		if !ok {
			http.Error(w, "no such key", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", obj.contentType)
		w.Header().Set("Last-Modified", obj.modified.Format(http.TimeFormat))
		w.Header().Set("Content-Length", fmt.Sprint(len(obj.data)))
		w.Header().Set("ETag", `"mini-etag"`)
		if r.Method == http.MethodGet {
			_, _ = w.Write(obj.data)
		}
	case http.MethodDelete:
		delete(m.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

func (m *miniS3) list(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := r.URL.Query().Get("prefix")
	var keys []string
	for key := range m.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	start := 0
	if token := r.URL.Query().Get("continuation-token"); token != "" {
		for i, key := range keys {
			if key > token {
				start = i
				break
			}
		}
	}
	end := start + m.pageSize
	if end > len(keys) {
		end = len(keys)
	}
	type content struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		ETag         string `xml:"ETag"`
		LastModified string `xml:"LastModified"`
	}
	result := struct {
		XMLName               xml.Name  `xml:"ListBucketResult"`
		IsTruncated           bool      `xml:"IsTruncated"`
		NextContinuationToken string    `xml:"NextContinuationToken,omitempty"`
		Contents              []content `xml:"Contents"`
	}{IsTruncated: end < len(keys)}
	for _, key := range keys[start:end] {
		obj := m.objects[key]
		result.Contents = append(result.Contents, content{
			Key:          key,
			Size:         int64(len(obj.data)),
			ETag:         `"mini-etag"`,
			LastModified: obj.modified.Format(time.RFC3339),
		})
	}
	if result.IsTruncated {
		result.NextContinuationToken = keys[end-1]
	}
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(result)
}

// verifySignature re-derives the SigV4 signature from the received request
// using the shared signer implementation and compares it to the presented
// Authorization header.
func (m *miniS3) verifySignature(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, sigAlgorithm+" ") {
		return fmt.Errorf("missing SigV4 authorization, got %q", auth)
	}
	presented := ""
	signedHeaders := ""
	for _, part := range strings.Split(strings.TrimPrefix(auth, sigAlgorithm+" "), ", ") {
		if value, ok := strings.CutPrefix(part, "Signature="); ok {
			presented = value
		}
		if value, ok := strings.CutPrefix(part, "SignedHeaders="); ok {
			signedHeaders = value
		}
	}
	amzDate := r.Header.Get("X-Amz-Date")
	at, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil {
		return fmt.Errorf("bad X-Amz-Date %q: %v", amzDate, err)
	}
	payloadSHA := r.Header.Get("X-Amz-Content-Sha256")

	var canonicalHeaders strings.Builder
	for _, name := range strings.Split(signedHeaders, ";") {
		value := r.Header.Get(name)
		if name == "host" {
			value = r.Host
		}
		canonicalHeaders.WriteString(name + ":" + strings.TrimSpace(value) + "\n")
	}
	canonical := strings.Join([]string{
		r.Method,
		canonicalURI(r.URL),
		canonicalQuery(r.URL.Query()),
		canonicalHeaders.String(),
		signedHeaders,
		payloadSHA,
	}, "\n")
	s := signer{keyID: testKeyID, secret: testSecret, region: "sgp1"}
	shortDate := at.UTC().Format("20060102")
	scope := shortDate + "/" + s.region + "/" + sigService + "/aws4_request"
	stringToSign := strings.Join([]string{sigAlgorithm, amzDate, scope, hexSHA256([]byte(canonical))}, "\n")
	want := fmt.Sprintf("%x", hmacSHA256(s.signingKey(shortDate), []byte(stringToSign)))
	if presented != want {
		return fmt.Errorf("signature mismatch for %s %s\ncanonical:\n%s", r.Method, r.URL, canonical)
	}
	return nil
}

// TestSpacesPresignVerifiable signs a URL with a pinned clock and checks the
// query-auth parameters land in the canonical wire form.
func TestSpacesPresignVerifiable(t *testing.T) {
	spaces := newTestSpaces(t)
	spaces.now = func() time.Time { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) }
	link, err := spaces.Presign(context.Background(), "GET", "sources/abc/bundle.git", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("X-Amz-Expires") != "900" || q.Get("X-Amz-Date") != "20260719T120000Z" || q.Get("X-Amz-SignedHeaders") != "host" {
		t.Fatalf("unexpected presign query: %s", link)
	}
	if u.Host != "kitsoki-test.sgp1.spaces.test" {
		t.Fatalf("presign host = %s", u.Host)
	}
}

func TestParseBucketURL(t *testing.T) {
	cfg, err := ParseBucketURL("https://kitsoki-test.sgp1.digitaloceanspaces.com")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bucket != "kitsoki-test" || cfg.Region != "sgp1" || cfg.Endpoint != "https://sgp1.digitaloceanspaces.com" {
		t.Fatalf("ParseBucketURL = %+v", cfg)
	}
	if _, err := ParseBucketURL("https://digitaloceanspaces.com"); err == nil {
		t.Fatal("expected error for non-virtual-hosted URL")
	}
}
