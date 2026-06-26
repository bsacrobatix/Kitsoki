package host

import (
	"context"
	"testing"
	"time"
)

func TestProviderQuotaBlocksConcurrentReservations(t *testing.T) {
	ctx := WithActiveProfile(context.Background(), ActiveProfile{
		Name: "synthetic-test",
		Provider: Provider{
			Model: "hf:test",
			Env: map[string]string{
				"ANTHROPIC_BASE_URL": "https://api.synthetic.new/anthropic",
			},
		},
		Quota: QuotaControl{
			Window:        "1m",
			MaxConcurrent: 1,
			ReserveTokens: 1,
		},
	})

	first, err := reserveProviderQuota(ctx, claudeBackend{}, "first")
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}

	secondDone := make(chan error, 1)
	go func() {
		second, err := reserveProviderQuota(ctx, claudeBackend{}, "second")
		if second != nil {
			second.finish(nil, "")
		}
		secondDone <- err
	}()

	select {
	case err := <-secondDone:
		t.Fatalf("second reservation completed while first was in flight: %v", err)
	case <-time.After(75 * time.Millisecond):
	}

	first.finish(nil, "")
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second reserve after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second reservation did not unblock after first finished")
	}
}

func TestProviderQuotaBacksOffAfterRateLimitError(t *testing.T) {
	ctx := WithActiveProfile(context.Background(), ActiveProfile{
		Name:     "synthetic-rate-limit-test",
		Provider: Provider{Model: "hf:test"},
		Quota: QuotaControl{
			Window:        "150ms",
			MaxConcurrent: 1,
			ReserveTokens: 1,
		},
	})

	first, err := reserveProviderQuota(ctx, claudeBackend{}, "first")
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	first.finish(nil, "429 rate limit")

	start := time.Now()
	second, err := reserveProviderQuota(ctx, claudeBackend{}, "second")
	if err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	second.finish(nil, "")
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("second reservation did not honor rate-limit backoff; elapsed=%s", elapsed)
	}
}
