package objectstore

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fake is an in-memory Store for tests and flows. It is safe for concurrent
// use and never touches the network.
type Fake struct {
	mu      sync.Mutex
	objects map[string]fakeObject
	// Now supplies timestamps; overridable so tests stay deterministic.
	Now func() time.Time
}

type fakeObject struct {
	data []byte
	meta Meta
}

func NewFake() *Fake {
	return &Fake{objects: map[string]fakeObject{}, Now: time.Now}
}

func (f *Fake) Put(ctx context.Context, key string, body io.Reader, size int64, opts PutOptions) (Meta, error) {
	if err := validateKey(key); err != nil {
		return Meta{}, err
	}
	if size < 0 {
		return Meta{}, fmt.Errorf("objectstore: negative size for %q", key)
	}
	data, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil {
		return Meta{}, err
	}
	if int64(len(data)) != size {
		return Meta{}, fmt.Errorf("objectstore: body length %d does not match declared size %d for %q", len(data), size, key)
	}
	sum := md5.Sum(data)
	meta := Meta{
		Key:          key,
		Size:         size,
		ETag:         hex.EncodeToString(sum[:]),
		ContentType:  opts.ContentType,
		LastModified: f.Now().UTC(),
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = fakeObject{data: data, meta: meta}
	return meta, nil
}

func (f *Fake) Get(ctx context.Context, key string) (io.ReadCloser, Meta, error) {
	f.mu.Lock()
	obj, ok := f.objects[key]
	f.mu.Unlock()
	if !ok {
		return nil, Meta{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return io.NopCloser(bytes.NewReader(obj.data)), obj.meta, nil
}

func (f *Fake) Head(ctx context.Context, key string) (Meta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[key]
	if !ok {
		return Meta{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return obj.meta, nil
}

func (f *Fake) List(ctx context.Context, prefix string) ([]Meta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Meta
	for key, obj := range f.objects {
		if strings.HasPrefix(key, prefix) {
			out = append(out, obj.meta)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *Fake) Delete(ctx context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	return nil
}

// Presign returns an opaque fake URL; consumers in tests only need a stable
// string that names the method and key.
func (f *Fake) Presign(ctx context.Context, method, key string, expiry time.Duration) (string, error) {
	if method != "GET" && method != "PUT" {
		return "", fmt.Errorf("objectstore: presign method %q unsupported", method)
	}
	if err := validateKey(key); err != nil {
		return "", err
	}
	return fmt.Sprintf("fake-presign://%s/%s?expires=%d", strings.ToLower(method), url.PathEscape(key), int64(expiry.Seconds())), nil
}

func validateKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "//") || strings.Contains(key, "..") {
		return fmt.Errorf("objectstore: invalid key %q", key)
	}
	return nil
}
