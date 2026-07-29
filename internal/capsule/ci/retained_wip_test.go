package ci

import (
	"bytes"
	"context"
	"testing"

	"kitsoki/internal/objectstore"
)

func TestReadRetainedWIPReadsOnlyCanonicalPoolObject(t *testing.T) {
	store := objectstore.NewFake()
	data := []byte("retained WIP bundle")
	if _, err := store.Put(context.Background(), "runs/exec-1/wip/refs.bundle", bytes.NewReader(data), int64(len(data)), objectstore.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	original := newPoolObjectStore
	newPoolObjectStore = func(string, SourceBucket) (objectstore.Store, error) { return store, nil }
	t.Cleanup(func() { newPoolObjectStore = original })
	cfg := Config{Remotes: map[string]Remote{"pool": {Pool: &PoolExecutor{SourceBucket: &SourceBucket{URL: "s3://bucket", KeyEnv: "KEY", SecretEnv: "SECRET"}}}}}
	got, err := ReadRetainedWIP(context.Background(), cfg, "pool", "exec-1")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("ReadRetainedWIP = %q, %v", got, err)
	}
}

func TestReadRetainedWIPFailsClosedForMissingUnsafeOrUnconfiguredOutput(t *testing.T) {
	store := objectstore.NewFake()
	original := newPoolObjectStore
	newPoolObjectStore = func(string, SourceBucket) (objectstore.Store, error) { return store, nil }
	t.Cleanup(func() { newPoolObjectStore = original })
	cfg := Config{Remotes: map[string]Remote{"pool": {Pool: &PoolExecutor{SourceBucket: &SourceBucket{URL: "s3://bucket", KeyEnv: "KEY", SecretEnv: "SECRET"}}}}}
	for _, tc := range []struct {
		name, executor, execution string
	}{
		{name: "missing", executor: "pool", execution: "exec-missing"},
		{name: "unsafe execution", executor: "pool", execution: "../other"},
		{name: "unconfigured executor", executor: "other", execution: "exec-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadRetainedWIP(context.Background(), cfg, tc.executor, tc.execution); err == nil {
				t.Fatal("ReadRetainedWIP unexpectedly succeeded")
			}
		})
	}
}
