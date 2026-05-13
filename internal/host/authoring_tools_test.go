package host_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/authoring"
	"kitsoki/internal/host"
)

// ─── fake AuthoringLedger ────────────────────────────────────────────────────

// fakeEntry implements host.LedgerEntry.
type fakeEntry struct {
	id string
	p  *authoring.Proposal
}

func (e *fakeEntry) ProposalID() string              { return e.id }
func (e *fakeEntry) Underlying() *authoring.Proposal { return e.p }

// fakeLedger implements host.AuthoringLedger and records every call
// so tests can assert which side-effects fired.
type fakeLedger struct {
	mu      sync.Mutex
	entries map[string]*fakeEntry
	// recorded mutations
	addedProposals []*authoring.Proposal
	appliedIDs     []string
	discardedIDs   []string
	discardErr     error
	nextIDPrefix   string
	nextIDCounter  int
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{
		entries:      make(map[string]*fakeEntry),
		nextIDPrefix: "fake",
	}
}

func (l *fakeLedger) Add(p *authoring.Proposal) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nextIDCounter++
	id := fmt.Sprintf("%s-%d", l.nextIDPrefix, l.nextIDCounter)
	l.entries[id] = &fakeEntry{id: id, p: p}
	l.addedProposals = append(l.addedProposals, p)
	return id
}

func (l *fakeLedger) Get(id string) (host.LedgerEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[id]
	if !ok {
		return nil, false
	}
	return e, true
}

func (l *fakeLedger) Discard(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.discardErr != nil {
		return l.discardErr
	}
	if _, ok := l.entries[id]; !ok {
		return fmt.Errorf("fakeLedger: unknown id %q", id)
	}
	delete(l.entries, id)
	l.discardedIDs = append(l.discardedIDs, id)
	return nil
}

func (l *fakeLedger) RecordApplied(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.appliedIDs = append(l.appliedIDs, id)
}

// ─── test fixture helpers ────────────────────────────────────────────────────

// fakeClaudeBin returns the absolute path to
// internal/authoring/testdata/fake-claude.sh. Re-uses the existing
// sibling fixture so we don't have to ship a duplicate.
func fakeClaudeBin(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-claude.sh requires bash")
	}
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	// internal/host/authoring_tools_test.go → ../authoring/testdata/...
	path := filepath.Join(filepath.Dir(thisFile), "..", "authoring", "testdata", "fake-claude.sh")
	abs, err := filepath.Abs(path)
	require.NoError(t, err)
	fi, err := os.Stat(abs)
	require.NoError(t, err, "fake-claude.sh not found at %s", abs)
	require.NotZero(t, fi.Mode()&0o111, "fake-claude.sh is not executable")
	return abs
}

// copyCloak makes a writable copy of the cloak app dir in a temp dir.
// Same shape as internal/authoring/authoring_test.go::copyCloak.
func copyCloak(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	src := filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "apps", "cloak")
	dst := t.TempDir()
	require.NoError(t, copyDir(src, dst))
	return filepath.Join(dst, "app.yaml")
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
}

// withFakeLedger registers a fake ledger under chatID for the duration
// of the test and unregisters it on cleanup.
func withFakeLedger(t *testing.T, chatID string) *fakeLedger {
	t.Helper()
	l := newFakeLedger()
	host.RegisterAuthoringLedger(chatID, l)
	t.Cleanup(func() {
		host.UnregisterAuthoringLedger(chatID)
	})
	return l
}

// ─── tests ───────────────────────────────────────────────────────────────────

// TestAuthoringPropose_HappyPath drives the full real-binary pipeline
// using the existing fake-claude.sh fixture: propose appends a YAML
// comment, the handler returns proposal_id + summary + diff, and the
// fake ledger has captured the underlying *authoring.Proposal.
func TestAuthoringPropose_HappyPath(t *testing.T) {
	t.Setenv(authoring.ClaudeBinEnv, fakeClaudeBin(t))
	appPath := copyCloak(t)
	ledger := withFakeLedger(t, "chat-1")

	out, err := host.AuthoringPropose(context.Background(), host.AuthoringProposeArgs{
		ChatID:  "chat-1",
		Text:    "ADD_LINE: append a marker",
		AppFile: appPath,
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.ProposalID, "propose returned no id")
	assert.Equal(t, "applied test edit", out.Summary)
	assert.Contains(t, out.Diff, "fake-claude appended this comment")

	// Cleanup the shadow dir the real authoring.Propose created.
	t.Cleanup(func() {
		if e, ok := ledger.entries[out.ProposalID]; ok && e.p != nil {
			_ = authoring.Discard(e.p)
		}
	})

	require.Len(t, ledger.addedProposals, 1, "exactly one proposal should be parked on the ledger")
}

// TestAuthoringPropose_UnknownChatID returns a clear error when no
// ledger is registered for the chat id.
func TestAuthoringPropose_UnknownChatID(t *testing.T) {
	_, err := host.AuthoringPropose(context.Background(), host.AuthoringProposeArgs{
		ChatID:  "no-such-chat",
		Text:    "anything",
		AppFile: "/tmp/whatever.yaml",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no ledger registered for chat_id "no-such-chat"`)
}

// TestAuthoringPropose_MissingChatID rejects empty chat_id.
func TestAuthoringPropose_MissingChatID(t *testing.T) {
	_, err := host.AuthoringPropose(context.Background(), host.AuthoringProposeArgs{
		Text:    "anything",
		AppFile: "/tmp/x.yaml",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chat_id is required")
}

// TestAuthoringApply_HappyPath uses a stubbed applyRunner so we don't
// actually write to disk. Asserts the handler returned applied=true,
// the ledger recorded the applied id, and the fake apply function
// saw the right *authoring.Proposal.
func TestAuthoringApply_HappyPath(t *testing.T) {
	ledger := withFakeLedger(t, "chat-2")
	dummy := &authoring.Proposal{Summary: "tidy-up", AppDir: "/tmp/fake-app", ShadowDir: "/tmp/fake-shadow"}
	id := ledger.Add(dummy)

	// Substitute the destructive apply for a no-op that captures the
	// proposal pointer.
	var seen *authoring.Proposal
	restore := host.OverrideApplyRunner(t, func(p *authoring.Proposal) error {
		seen = p
		return nil
	})
	defer restore()

	out, err := host.AuthoringApply(context.Background(), host.AuthoringApplyArgs{
		ChatID:     "chat-2",
		ProposalID: id,
	})
	require.NoError(t, err)
	assert.True(t, out.Applied)
	assert.Equal(t, "tidy-up", out.Summary)
	assert.Equal(t, dummy, seen, "applyRunner should have been called with the ledger's proposal")
	assert.Equal(t, []string{id}, ledger.appliedIDs, "ledger.RecordApplied should have been called once with the id")
}

// TestAuthoringApply_UnknownProposalID returns Applied:false plus a
// structured Go error (the brief: "structured error result, not a Go
// error" — we represent that by setting Applied:false AND returning
// the error so callers can pattern-match on either). The host
// handler-shape wrapper translates this into a Result.Error string.
func TestAuthoringApply_UnknownProposalID(t *testing.T) {
	withFakeLedger(t, "chat-3")

	out, err := host.AuthoringApply(context.Background(), host.AuthoringApplyArgs{
		ChatID:     "chat-3",
		ProposalID: "nonexistent",
	})
	require.Error(t, err)
	assert.False(t, out.Applied)
	assert.Contains(t, err.Error(), `unknown proposal_id "nonexistent"`)
}

// TestAuthoringApply_NoLedger surfaces a clear error when the chat id
// is not registered (e.g. the controller never wrapped Send around
// this call).
func TestAuthoringApply_NoLedger(t *testing.T) {
	_, err := host.AuthoringApply(context.Background(), host.AuthoringApplyArgs{
		ChatID:     "no-such",
		ProposalID: "anything",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no ledger registered")
}

// TestAuthoringDiscard_HappyPath drops a proposal from the ledger and
// trusts ledger.Discard to call authoring.Discard internally (verified
// elsewhere; here we just assert the handler's wiring).
func TestAuthoringDiscard_HappyPath(t *testing.T) {
	ledger := withFakeLedger(t, "chat-4")
	dummy := &authoring.Proposal{Summary: "scrap"}
	id := ledger.Add(dummy)

	out, err := host.AuthoringDiscard(context.Background(), host.AuthoringDiscardArgs{
		ChatID:     "chat-4",
		ProposalID: id,
	})
	require.NoError(t, err)
	assert.True(t, out.Discarded)
	assert.Equal(t, []string{id}, ledger.discardedIDs, "ledger.Discard should have been called with the id")
}

// TestAuthoringDiscard_UnknownProposalID returns Discarded:false plus
// a Go error matching the unknown-id pattern.
func TestAuthoringDiscard_UnknownProposalID(t *testing.T) {
	withFakeLedger(t, "chat-5")
	out, err := host.AuthoringDiscard(context.Background(), host.AuthoringDiscardArgs{
		ChatID:     "chat-5",
		ProposalID: "missing",
	})
	require.Error(t, err)
	assert.False(t, out.Discarded)
	assert.Contains(t, err.Error(), `unknown proposal_id "missing"`)
}

// TestRegister_AuthoringBuiltins exercises the host.Handler adapters
// that RegisterBuiltins installs, exercising the JSON-decode +
// Result.Data shape path.
func TestRegister_AuthoringBuiltins_HandlerAdapters(t *testing.T) {
	r := host.NewRegistry()
	host.RegisterAuthoringBuiltins(r)

	proposeHandler, ok := r.Get("host.authoring.propose")
	require.True(t, ok, "host.authoring.propose not registered")
	applyHandler, ok := r.Get("host.authoring.apply")
	require.True(t, ok, "host.authoring.apply not registered")
	discardHandler, ok := r.Get("host.authoring.discard")
	require.True(t, ok, "host.authoring.discard not registered")

	// Missing ledger surfaces via Result.Error (the handler shape's
	// contract — domain errors flow through Error, infra failures
	// through Go errors).
	res, err := proposeHandler(context.Background(), map[string]any{
		"chat_id":  "nope",
		"app_file": "/tmp/x.yaml",
		"text":     "anything",
	})
	require.NoError(t, err)
	assert.Contains(t, res.Error, "no ledger registered")

	// Apply / discard with missing chat_id surface via Result.Error too.
	res, _ = applyHandler(context.Background(), map[string]any{"proposal_id": "x"})
	assert.Contains(t, res.Error, "chat_id is required")
	res, _ = discardHandler(context.Background(), map[string]any{"proposal_id": "x"})
	assert.Contains(t, res.Error, "chat_id is required")
}

// TestParseAuthoringCalls exercises the structured-token parser
// claude's reply is run through. Verifies ordering, multi-call
// dispatch, and graceful skipping of malformed blocks.
func TestParseAuthoringCalls(t *testing.T) {
	reply := `Sure, I'll draft that.

<<<propose>>>{"app_file":"/tmp/app.yaml","text":"change the foyer"}<<<endpropose>>>

And to apply the previous draft:

<<<apply abcd1234>>>

Also discard the stale one:

<<<discard ffff0000>>>
`
	calls := host.ParseAuthoringCalls(reply)
	require.Len(t, calls, 3, "expected three calls")

	assert.Equal(t, host.AuthoringCallPropose, calls[0].Kind)
	assert.Contains(t, calls[0].Payload, `"text":"change the foyer"`)

	assert.Equal(t, host.AuthoringCallApply, calls[1].Kind)
	assert.Equal(t, "abcd1234", calls[1].ProposalID)

	assert.Equal(t, host.AuthoringCallDiscard, calls[2].Kind)
	assert.Equal(t, "ffff0000", calls[2].ProposalID)
}

func TestParseAuthoringCalls_Empty(t *testing.T) {
	assert.Empty(t, host.ParseAuthoringCalls(""))
	assert.Empty(t, host.ParseAuthoringCalls("hello, no tokens here"))
}

// TestParseAuthoringCalls_EmptyProposeBody skips a propose with no
// JSON body rather than emitting a malformed call entry.
func TestParseAuthoringCalls_EmptyProposeBody(t *testing.T) {
	calls := host.ParseAuthoringCalls("<<<propose>>><<<endpropose>>>")
	assert.Empty(t, calls)
}

// TestAuthoringPropose_NilProposalFromRunner asserts the handler
// guards against a runner that returns (nil, nil) — should never
// happen in real life but cheap to defend.
func TestAuthoringPropose_NilProposalFromRunner(t *testing.T) {
	withFakeLedger(t, "chat-nilp")
	restore := host.OverrideProposeRunner(t, func(ctx context.Context, appPath, text string, runCtx *authoring.Context) (*authoring.Proposal, error) {
		return nil, nil
	})
	defer restore()

	_, err := host.AuthoringPropose(context.Background(), host.AuthoringProposeArgs{
		ChatID:  "chat-nilp",
		Text:    "x",
		AppFile: "/tmp/x.yaml",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil proposal")
}

// TestAuthoringPropose_RunnerError wraps the underlying error so the
// agent gets a recognisable "host.authoring.propose: …" prefix.
func TestAuthoringPropose_RunnerError(t *testing.T) {
	withFakeLedger(t, "chat-rerr")
	sentinel := errors.New("boom")
	restore := host.OverrideProposeRunner(t, func(ctx context.Context, appPath, text string, runCtx *authoring.Context) (*authoring.Proposal, error) {
		return nil, sentinel
	})
	defer restore()

	_, err := host.AuthoringPropose(context.Background(), host.AuthoringProposeArgs{
		ChatID:  "chat-rerr",
		Text:    "x",
		AppFile: "/tmp/x.yaml",
	})
	require.Error(t, err)
	if !strings.Contains(err.Error(), "host.authoring.propose") || !errors.Is(err, sentinel) {
		t.Errorf("error did not wrap sentinel with the right prefix: %v", err)
	}
}
