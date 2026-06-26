package host

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	defaultQuotaWindow        = time.Minute
	defaultQuotaReserveTokens = int64(12000)
	quotaTokenChars           = 4
)

type quotaLimiter struct {
	mu              sync.Mutex
	window          time.Duration
	tokensPerWindow int64
	maxConcurrent   int
	inFlight        int
	windowStart     time.Time
	windowTokens    int64
	observedCalls   int64
	observedTokens  int64
	backoffUntil    time.Time
	lastThrottleLog time.Time
}

type quotaReservation struct {
	limiter   *quotaLimiter
	key       string
	estimate  int64
	startTime time.Time
}

var providerQuotaLimiters sync.Map

func providerQuotaKey(ctx context.Context, backend agentBackend) (string, QuotaControl, bool) {
	prof, ok := ActiveProfileFromContext(ctx)
	if !ok {
		return "", QuotaControl{}, false
	}
	q := prof.Quota
	if q == (QuotaControl{}) {
		return "", QuotaControl{}, false
	}
	model := strings.TrimSpace(prof.Provider.Model)
	if model == "" {
		model = "default"
	}
	key := prof.Name + "|" + backend.Name() + "|" + model + "|" + providerEndpoint(prof.Provider.Env)
	return key, q, true
}

func providerEndpoint(env map[string]string) string {
	for _, k := range []string{"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL"} {
		if v := strings.TrimSpace(env[k]); v != "" {
			return v
		}
	}
	return "ambient"
}

func estimatePromptTokens(stdin string, q QuotaControl) int64 {
	est := int64(len(stdin)/quotaTokenChars) + 1
	reserve := q.ReserveTokens
	if reserve <= 0 {
		reserve = defaultQuotaReserveTokens
	}
	if est < reserve {
		return reserve
	}
	return est
}

func reserveProviderQuota(ctx context.Context, backend agentBackend, stdin string) (*quotaReservation, error) {
	key, q, ok := providerQuotaKey(ctx, backend)
	if !ok {
		return nil, nil
	}
	lim := limiterForProvider(key, q)
	est := estimatePromptTokens(stdin, q)
	if err := lim.reserve(ctx, key, est); err != nil {
		return nil, err
	}
	return &quotaReservation{limiter: lim, key: key, estimate: est, startTime: time.Now()}, nil
}

func limiterForProvider(key string, q QuotaControl) *quotaLimiter {
	v, _ := providerQuotaLimiters.LoadOrStore(key, &quotaLimiter{})
	lim := v.(*quotaLimiter)
	lim.configure(q)
	return lim
}

func (l *quotaLimiter) configure(q QuotaControl) {
	l.mu.Lock()
	defer l.mu.Unlock()
	window := defaultQuotaWindow
	if q.Window != "" {
		if parsed, err := time.ParseDuration(q.Window); err == nil && parsed > 0 {
			window = parsed
		}
	}
	l.window = window
	l.tokensPerWindow = q.TokensPerWindow
	l.maxConcurrent = q.MaxConcurrent
	if l.windowStart.IsZero() {
		l.windowStart = time.Now()
	}
}

func (l *quotaLimiter) reserve(ctx context.Context, key string, tokens int64) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		wait, ok := l.tryReserve(key, tokens)
		if ok {
			return nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-ticker.C:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (l *quotaLimiter) tryReserve(key string, tokens int64) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.rollWindow(now)
	if l.observedCalls > 0 {
		if avg := l.observedTokens / l.observedCalls; avg > tokens {
			tokens = avg
		}
	}
	if now.Before(l.backoffUntil) {
		wait := l.backoffUntil.Sub(now)
		l.logThrottleLocked(now, key, wait, "backoff")
		return wait, false
	}
	if l.maxConcurrent > 0 && l.inFlight >= l.maxConcurrent {
		l.logThrottleLocked(now, key, time.Second, "concurrency")
		return time.Second, false
	}
	if l.tokensPerWindow > 0 && l.windowTokens+tokens > l.tokensPerWindow {
		wait := l.windowStart.Add(l.window).Sub(now)
		if wait < 250*time.Millisecond {
			wait = 250 * time.Millisecond
		}
		l.logThrottleLocked(now, key, wait, "tokens")
		return wait, false
	}
	l.inFlight++
	l.windowTokens += tokens
	return 0, true
}

func (l *quotaLimiter) rollWindow(now time.Time) {
	if l.window <= 0 {
		l.window = defaultQuotaWindow
	}
	if l.windowStart.IsZero() || now.Sub(l.windowStart) >= l.window {
		l.windowStart = now
		l.windowTokens = 0
	}
}

func (l *quotaLimiter) logThrottleLocked(now time.Time, key string, wait time.Duration, reason string) {
	if now.Sub(l.lastThrottleLog) < 5*time.Second {
		return
	}
	l.lastThrottleLog = now
	slog.Info("provider.quota.throttle",
		"profile_key", key,
		"reason", reason,
		"wait_ms", wait.Milliseconds(),
		"in_flight", l.inFlight,
		"window_tokens", l.windowTokens,
		"tokens_per_window", l.tokensPerWindow,
	)
}

func (r *quotaReservation) finish(usage map[string]any, errText string) {
	if r == nil || r.limiter == nil {
		return
	}
	r.limiter.finish(r.key, r.estimate, usage, errText, time.Since(r.startTime))
}

func (l *quotaLimiter) finish(key string, estimate int64, usage map[string]any, errText string, duration time.Duration) {
	observed := usageTotalTokens(usage)
	rateLimited := looksRateLimited(errText)
	l.mu.Lock()
	if l.inFlight > 0 {
		l.inFlight--
	}
	if observed > 0 {
		l.observedCalls++
		l.observedTokens += observed
	}
	if rateLimited {
		l.backoffUntil = time.Now().Add(l.window)
	}
	calls, tokens, inFlight := l.observedCalls, l.observedTokens, l.inFlight
	l.mu.Unlock()
	slog.Info("provider.quota.usage",
		"profile_key", key,
		"estimated_tokens", estimate,
		"observed_tokens", observed,
		"observed_calls", calls,
		"observed_total_tokens", tokens,
		"duration_ms", duration.Milliseconds(),
		"in_flight", inFlight,
		"rate_limited", rateLimited,
	)
}

func usageTotalTokens(usage map[string]any) int64 {
	if usage == nil {
		return 0
	}
	if total := usageInt64(usage, "total_tokens"); total > 0 {
		return total
	}
	var total int64
	for _, k := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "cached_input_tokens", "reasoning_output_tokens"} {
		total += usageInt64(usage, k)
	}
	return total
}

func usageInt64(usage map[string]any, key string) int64 {
	switch v := usage[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	default:
		return 0
	}
}

func looksRateLimited(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "rate limit") ||
		strings.Contains(s, "rate_limit") ||
		strings.Contains(s, "quota") ||
		strings.Contains(s, "too many requests") ||
		strings.Contains(s, "429")
}
