// registry.go — SessionRegistry, the concrete server.SessionProvider for
// `kitsoki web`'s multi-story surface.
//
// The registry lives in package main BECAUSE it must call buildSessionRuntime
// (runtime.go) and use runtimeConfig, and an internal/ package cannot import
// package main. This is the import-direction inversion the decomposition calls
// out: the server package DEFINES server.SessionProvider, and this concrete
// implementation DEPENDS on it.
//
// One registry owns:
//
//   - a catalogue of discovered stories (webconfig.StoryMeta), refreshed by an
//     explicit Rescan — there is no fsnotify watch (decided lean);
//   - a set of live sessions, each an *entry* keyed by a fresh UUID, holding the
//     story it runs, its *sessionRuntime, the read Source and write Driver the
//     server routes against, and enough state to drive a TUI-parity Reload.
//
// Sessions are in-memory only: they die with the process (no persistence
// across restarts, no kill action — decided leans for the PoC). Live count IS
// now bounded (swarm-session-cap): session.new / AttachExternal evict the
// least-recently-active IDLE session once the configurable cap is reached
// (server.SessionRegistry.ensureCapacityLocked), so dozens of churning swarm
// UI-QA sessions can't leak an orchestrator per session for the life of the
// process the way an uncapped registry would.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/pmezard/go-difflib/difflib"

	"kitsoki/internal/agentroot"
	"kitsoki/internal/agents"
	"kitsoki/internal/app"
	"kitsoki/internal/applicationconversation"
	"kitsoki/internal/applicationjob"
	"kitsoki/internal/artifactjob"
	"kitsoki/internal/campaign"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/chats"
	"kitsoki/internal/clock"
	"kitsoki/internal/compliance"
	"kitsoki/internal/daemonfederation"
	"kitsoki/internal/host"
	"kitsoki/internal/jobs"
	"kitsoki/internal/materializationstatus"
	"kitsoki/internal/metamode"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/reviewedfeedback"
	"kitsoki/internal/runstatus"
	"kitsoki/internal/runstatus/server"
	"kitsoki/internal/store"
	"kitsoki/internal/storydemo"
	"kitsoki/internal/study"
	"kitsoki/internal/testrunner"
	"kitsoki/internal/webconfig"
	"kitsoki/internal/workerregistry"
)

// entry is one live session as the registry owns it. The server only needs the
// Source + Driver (exposed via server.Entry); the registry retains the rest to
// drive the lifecycle:
//
//   - StoryPath is the absolute app.yaml path — the Reload target and the key
//     that links a session back to its StoryHeader for the active-session count.
//   - Def is the (possibly reloaded) app definition for display.
//   - rt is the owned *sessionRuntime; Close releases it on shutdown.
//   - sid is the orchestrator session id Reload / driver calls bind to.
//   - source is the LiveSession; Reload reads currentState from its snapshot.
type entry struct {
	StoryPath     string
	Def           *app.AppDef
	synthetic     bool
	externalKey   string // transport:thread for store-attached sessions; "" for fresh ones
	loadedContent []byte // raw app.yaml bytes at last load/reload, for staleness check
	rt            *sessionRuntime
	sid           app.SessionID
	source        *server.LiveSession
	driver        server.Driver
	sink          *store.JSONLSink
	sessionDir    string // directory holding this session's trace + sidecars

	// metaController is the lazily-built meta-mode controller for this
	// session, cached so the persistent chat store / agent registry / AppDef
	// binding survives across turns. Reload nils it so the next meta turn
	// rebuilds against the reloaded AppDef.
	metaController *metamode.Controller

	// frames / feedback are the lazily-built /review feedback-mode seams,
	// cached so the still recorder's seq counter and the feedback sidecar path
	// stay stable across RPCs for this session.
	frames   *server.JournalFrameRecorder
	feedback *server.JSONLFeedbackSink

	// turnsInFlight counts driver calls currently executing against this
	// session (Turn/SubmitDirect/ContinueTurn/AskOffPath/Teleport/RewindRoute,
	// via trackingDriver). >0 means "mid-turn" — ensureCapacityLocked never
	// picks such a session as an eviction victim. Accessed with sync/atomic so
	// trackingDriver's begin/end calls don't need the registry mutex held for
	// the call's whole duration.
	turnsInFlight int32

	// lastActive is stamped by trackingDriver when a turn-advancing call
	// completes (and seeded to session-creation time), so
	// ensureCapacityLocked's "least-recently-active idle session" choice has a
	// meaningful signal — a session mid-conversation ranks recent even between
	// turns; one nobody has touched since it was opened ranks oldest. Guarded
	// by the registry mutex (all entry field access is).
	lastActive time.Time
}

type storyLoad struct {
	path      string
	def       *app.AppDef
	raw       []byte
	reloader  func() (*app.AppDef, error)
	synthetic bool
	repoRoot  string
}

// frameRecorderLocked returns the session's still recorder, building it on first
// use. Stills land under <sessionDir>/frames; the journal writer makes them
// resolve through the existing /artifact/{id} route. Caller holds the registry
// mutex (Get does).
func (e *entry) frameRecorderLocked() *server.JournalFrameRecorder {
	if e.frames == nil {
		e.frames = &server.JournalFrameRecorder{
			Writer:    e.rt.Journal,
			SID:       e.sid,
			FramesDir: filepath.Join(e.sessionDir, "frames"),
		}
	}
	return e.frames
}

// feedbackSinkLocked returns the session's append-only feedback sink, building
// it on first use. Notes land in <sessionDir>/<sid>.feedback.jsonl, the file the
// slice-3 authoring story drains on its next refine turn. Caller holds the
// registry mutex.
func (e *entry) feedbackSinkLocked() *server.JSONLFeedbackSink {
	if e.feedback == nil {
		e.feedback = &server.JSONLFeedbackSink{
			Path: filepath.Join(e.sessionDir, string(e.sid)+".feedback.jsonl"),
		}
	}
	return e.feedback
}

// SessionRegistry implements [server.SessionProvider]. It is safe for concurrent
// use: a single mutex guards both the story catalogue and the live-session map,
// since the SSE pollers and RPC handlers call in from many goroutines.
type SessionRegistry struct {
	cfg  webconfig.WebConfig
	base runtimeBase
	dirs []string

	// notifier, when set, attaches a cross-session notification relay to each new
	// session's orchestrator (server.Notifier, injected by web.go via
	// SetNotifier). Read without the lock — set once at startup before any
	// NewSession call, never mutated after.
	notifier server.Notifier

	mu       sync.Mutex
	stories  []webconfig.StoryMeta
	sessions map[string]*entry

	// maxSessions caps the live-session count (swarm-session-cap). Set from
	// $KITSOKI_WEB_MAX_SESSIONS (or DefaultMaxLiveSessions) at construction;
	// SetMaxSessions overrides it (e.g. from a future --max-sessions flag, or
	// a test tightening it to exercise eviction cheaply). Guarded by mu.
	maxSessions int

	// currentSessionID is the id of the most recently created (NewSession) or
	// attached (AttachExternal) session — the "current" session trace-only and
	// graph-only surfaces follow (server.CurrentSessionProvider). Empty means no
	// session yet. Guarded by mu.
	currentSessionID string

	// Meta-mode shared resources, all guarded by mu. agentReg is the builtin
	// agent registry every meta controller resolves names against. The self*
	// fields back the home-screen (session-less) meta driver for the cross-app
	// kitsoki.* modes; they are opened lazily on first home-screen meta use.
	agentReg     agents.Registry
	metaSelfStr  store.Store
	metaSelfChat *chats.Store
	metaSelfCtrl *metamode.Controller

	// daemonStore owns the connection used by daemonJobs. It is separate from
	// each live session runtime but points at the same SQLite file, so durable
	// job identity survives registry and process teardown.
	daemonStore                  store.Store
	daemonJobs                   artifactjob.Store
	campaignStore                campaign.Store
	campaignSource               campaign.Source
	campaignScheduler            jobs.Scheduler
	campaignServices             map[string]*campaign.Service
	studies                      study.Store
	federation                   *daemonfederation.Pool
	materializations             materializationstatus.Store
	feedbackDispatches           reviewedfeedback.DispatchStore
	feedbackReconciles           reviewedfeedback.ReconcileStore
	feedbackLedger               reviewedfeedback.JSONLLedger
	applicationConversationStore *applicationconversation.SQLStore
	applicationConversationChats *chats.Store

	// feedbackBackends are explicit daemon-construction bindings keyed by
	// application ID. Session construction derives the remaining scope from
	// the loaded application metadata.
	feedbackBackends           map[string]host.FeedbackBackend
	feedbackFederationBackends map[string]host.FeedbackBackend
	feedbackCaptureSources     map[string]reviewedfeedback.CaptureSource

	// flowEvidenceProviders are explicit daemon-construction bindings. The
	// registered catalog path and dependencies are never selected by callers.
	flowEvidenceProviders map[string]host.FlowEvidenceProvider

	applicationArtifactMu sync.Mutex
	applicationBundleRoot string
	applicationJobs       *applicationjob.Service
}

// NewRegistry constructs a registry over the resolved story dirs. cfg carries
// the (already loaded) WebConfig; dirs is the resolved story-dir list (flags >
// config > default — resolved by the caller via webconfig.Resolve). base is the
// session-invariant construction posture every new session inherits. The
// initial catalogue is empty until the caller runs Rescan.
func NewRegistry(cfg webconfig.WebConfig, dirs []string, base runtimeBase) *SessionRegistry {
	base.StoryDemoBindings = make(map[string]storydemo.DeploymentBinding, len(cfg.StoryApplicationArtifacts))
	for caller, binding := range cfg.StoryApplicationArtifacts {
		mockupApplicationID := ""
		if binding.CreateMockup != nil {
			mockupApplicationID = binding.CreateMockup.ApplicationID
		}
		base.StoryDemoBindings[caller] = storydemo.DeploymentBinding{
			CatalogPath:         binding.Catalog,
			CatalogRef:          binding.CatalogRef,
			MockupApplicationID: mockupApplicationID,
		}
	}
	return &SessionRegistry{
		cfg:                        cfg,
		base:                       base,
		dirs:                       dirs,
		sessions:                   map[string]*entry{},
		feedbackBackends:           map[string]host.FeedbackBackend{},
		feedbackFederationBackends: map[string]host.FeedbackBackend{},
		feedbackCaptureSources:     map[string]reviewedfeedback.CaptureSource{},
		flowEvidenceProviders:      map[string]host.FlowEvidenceProvider{},
		maxSessions:                maxSessionsFromEnv(),
	}
}

// RegisterFeedbackCaptureSource binds an opaque semantic source ID to an
// existing platform-owned typed feedback source. Configuration and host calls
// cannot supply a path, URL, transport, credential, or provider.
func (r *SessionRegistry) RegisterFeedbackCaptureSource(
	sourceID string,
	source reviewedfeedback.CaptureSource,
) error {
	sourceID = strings.TrimSpace(sourceID)
	if !validFeedbackServiceID(sourceID) {
		return errors.New("register feedback capture source: opaque source id is required")
	}
	if sourceID == reviewedfeedback.ApplicationFeedbackSourceID {
		return fmt.Errorf(
			"register feedback capture source %q: source id is reserved by the platform",
			sourceID,
		)
	}
	if source == nil {
		return fmt.Errorf("register feedback capture source %q: source is required", sourceID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.feedbackCaptureSources == nil {
		r.feedbackCaptureSources = make(map[string]reviewedfeedback.CaptureSource)
	}
	if _, exists := r.feedbackCaptureSources[sourceID]; exists {
		return fmt.Errorf("register feedback capture source %q: already registered", sourceID)
	}
	r.feedbackCaptureSources[sourceID] = source
	return nil
}

func validFeedbackServiceID(value string) bool {
	if value == "" || len(value) > 180 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == ':':
		default:
			return false
		}
	}
	return true
}

// RegisterFeedbackBackend binds one application ID to a governed feedback
// backend. It intentionally does not infer story names, repositories, or
// launcher paths. Register bindings during daemon construction before sessions
// are created.
func (r *SessionRegistry) RegisterFeedbackBackend(appID string, backend host.FeedbackBackend) error {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return errors.New("register feedback backend: application id is required")
	}
	if backend == nil {
		return fmt.Errorf("register feedback backend %q: backend is required", appID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.feedbackBackends == nil {
		r.feedbackBackends = make(map[string]host.FeedbackBackend)
	}
	if _, exists := r.feedbackBackends[appID]; exists {
		return fmt.Errorf("register feedback backend %q: already registered", appID)
	}
	r.feedbackBackends[appID] = backend
	return nil
}

// RegisterFlowEvidenceProvider binds one application to an exact catalog and
// an injected deterministic runner, resolver, durable store, and clock.
func (r *SessionRegistry) RegisterFlowEvidenceProvider(
	appID string,
	provider host.FlowEvidenceProvider,
) error {
	appID = strings.TrimSpace(appID)
	provider.CatalogPath = strings.TrimSpace(provider.CatalogPath)
	if appID == "" {
		return errors.New("register flow evidence provider: application id is required")
	}
	if provider.CatalogPath == "" || provider.Resolver == nil ||
		provider.Runner == nil || provider.Store == nil || provider.Clock == nil {
		return fmt.Errorf("register flow evidence provider %q: complete injected dependencies are required", appID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.flowEvidenceProviders == nil {
		r.flowEvidenceProviders = make(map[string]host.FlowEvidenceProvider)
	}
	if _, exists := r.flowEvidenceProviders[appID]; exists {
		return fmt.Errorf("register flow evidence provider %q: already registered", appID)
	}
	r.flowEvidenceProviders[appID] = provider
	return nil
}

// EnableDaemon turns the registry into the persistent daemon session owner.
// It is explicit rather than an environment toggle so ordinary `kitsoki web`
// keeps its existing process-local behavior.
func (r *SessionRegistry) EnableDaemon(dbPath string) error {
	st, err := openSessionStoreBackend(dbPath)
	if err != nil {
		return fmt.Errorf("open daemon store: %w", err)
	}
	artifactJobs, err := newArtifactJobStore(st)
	if err != nil {
		_ = st.Close()
		return fmt.Errorf("open daemon artifact jobs: %w", err)
	}
	r.daemonStore = st
	r.daemonJobs = artifactJobs
	if len(r.cfg.StoryApplicationJobs) > 0 {
		applicationJobRecords, applicationJobErr := newApplicationJobStore(st)
		if applicationJobErr != nil {
			_ = st.Close()
			return fmt.Errorf("open daemon application jobs: %w", applicationJobErr)
		}
		r.applicationJobs = &applicationjob.Service{
			Records:   applicationJobRecords,
			Jobs:      artifactJobs,
			Backend:   applicationJobRegistryBackend{registry: r},
			Templates: r.cfg.StoryApplicationJobs,
			Now:       time.Now,
		}
	}
	if len(r.cfg.ApplicationConversations) > 0 {
		conversationStore, conversationErr := newApplicationConversationStore(st, clock.Real())
		if conversationErr != nil {
			_ = st.Close()
			return fmt.Errorf("open daemon application conversations: %w", conversationErr)
		}
		conversationChats, conversationErr := newChatStore(st)
		if conversationErr != nil {
			_ = st.Close()
			return fmt.Errorf("open daemon application conversation chats: %w", conversationErr)
		}
		if _, conversationErr := conversationStore.InterruptPending(
			context.Background(), "daemon_restarted",
		); conversationErr != nil {
			_ = st.Close()
			return fmt.Errorf("restore daemon application conversations: %w", conversationErr)
		}
		r.applicationConversationStore = conversationStore
		r.applicationConversationChats = conversationChats
	}
	if len(r.cfg.ReviewedFeedback) > 0 || len(r.cfg.FeedbackFederation) > 0 {
		feedbackDispatches, feedbackErr := newReviewedFeedbackDispatchStore(
			st, clock.Real(),
		)
		if feedbackErr != nil {
			_ = st.Close()
			return fmt.Errorf("open daemon reviewed feedback dispatches: %w", feedbackErr)
		}
		if _, feedbackErr := feedbackDispatches.InterruptPending(
			context.Background(), "daemon_restarted",
		); feedbackErr != nil {
			_ = st.Close()
			return fmt.Errorf("restore daemon reviewed feedback dispatches: %w", feedbackErr)
		}
		r.feedbackDispatches = feedbackDispatches
	}
	if len(r.cfg.ReviewedFeedback) > 0 || len(r.cfg.FeedbackIntake) > 0 ||
		len(r.cfg.FeedbackFederation) > 0 {
		feedbackReconciles, feedbackErr := newReviewedFeedbackReconcileStore(
			st, clock.Real(),
		)
		if feedbackErr != nil {
			_ = st.Close()
			return fmt.Errorf("open daemon feedback reconciliations: %w", feedbackErr)
		}
		if _, feedbackErr := feedbackReconciles.InterruptPending(
			context.Background(), "daemon_restarted",
		); feedbackErr != nil {
			_ = st.Close()
			return fmt.Errorf("restore daemon feedback reconciliations: %w", feedbackErr)
		}
		r.feedbackReconciles = feedbackReconciles
	}
	r.base.StoryDemoExecutor = r
	if root, rootErr := os.Getwd(); rootErr == nil {
		r.applicationBundleRoot = filepath.Join(root, ".artifacts", "application-builds")
	}
	if r.cfg.Campaigns != nil {
		campaignStore, campaignErr := campaign.NewSQLiteStore(st.DB())
		if campaignErr != nil {
			_ = st.Close()
			return fmt.Errorf("open daemon campaigns: %w", campaignErr)
		}
		catalogPath, campaignErr := filepath.Abs(r.cfg.Campaigns.Catalog)
		if campaignErr != nil {
			_ = st.Close()
			return fmt.Errorf("resolve daemon campaign catalog: %w", campaignErr)
		}
		campaignJobs, campaignErr := jobs.NewJobStore(st.DB())
		if campaignErr != nil {
			_ = st.Close()
			return fmt.Errorf("open daemon campaign scheduler: %w", campaignErr)
		}
		r.campaignStore = campaignStore
		r.campaignSource = campaign.CatalogSource{
			Path:           catalogPath,
			TypeID:         r.cfg.Campaigns.TypeID,
			MaxDefinitions: r.cfg.Campaigns.MaxDefinitions,
			MaxBytes:       r.cfg.Campaigns.MaxBytes,
		}
		r.campaignScheduler = jobs.NewScheduler(campaignJobs)
		r.campaignServices = make(map[string]*campaign.Service)
		if _, campaignErr := campaignStore.InterruptRunning(
			context.Background(), "daemon_restarted", time.Now().UTC(),
		); campaignErr != nil {
			_ = st.Close()
			return fmt.Errorf("restore daemon campaigns: %w", campaignErr)
		}
	}
	studies, err := newStudyStore(st)
	if err != nil {
		_ = st.Close()
		return fmt.Errorf("open daemon studies: %w", err)
	}
	r.studies = studies
	if _, ok := r.configuredMaterializationApplication(); ok {
		materializations, err := newMaterializationStatusStore(st, time.Now)
		if err != nil {
			_ = st.Close()
			return fmt.Errorf("open daemon materialization projection: %w", err)
		}
		if _, err := materializations.InterruptActive(context.Background(), "daemon_restarted"); err != nil {
			_ = st.Close()
			return fmt.Errorf("restore daemon materialization projection: %w", err)
		}
		r.materializations = materializations
	}
	return nil
}

func (r *SessionRegistry) SubmitStudy(ctx context.Context, req study.SubmitRequest) (study.Study, bool, error) {
	if r.studies == nil {
		return study.Study{}, false, errors.New("study coordinator is unavailable outside daemon mode")
	}
	return r.studies.Submit(ctx, req)
}
func (r *SessionRegistry) GetStudy(ctx context.Context, id string) (study.Snapshot, error) {
	if r.studies == nil {
		return study.Snapshot{}, errors.New("study coordinator is unavailable outside daemon mode")
	}
	return r.studies.Get(ctx, id)
}
func (r *SessionRegistry) ListStudies(ctx context.Context) ([]study.Study, error) {
	if r.studies == nil {
		return []study.Study{}, nil
	}
	return r.studies.List(ctx)
}
func (r *SessionRegistry) StudyEvents(ctx context.Context, id string, since int64) ([]study.Event, error) {
	if r.studies == nil {
		return []study.Event{}, nil
	}
	return r.studies.Events(ctx, id, since)
}
func (r *SessionRegistry) RetryStudyCell(ctx context.Context, id, cell string) (study.Attempt, error) {
	if r.studies == nil {
		return study.Attempt{}, errors.New("study coordinator is unavailable outside daemon mode")
	}
	return r.studies.Retry(ctx, id, cell)
}
func (r *SessionRegistry) CancelStudyCell(ctx context.Context, id, cell string) error {
	if r.studies == nil {
		return errors.New("study coordinator is unavailable outside daemon mode")
	}
	return r.studies.Cancel(ctx, id, cell)
}

func (r *SessionRegistry) SetDaemonFederation(pool *daemonfederation.Pool) {
	r.federation = pool
}

// DefaultMaxLiveSessions is the fallback cap on concurrently live in-memory
// sessions (swarm-session-cap) when neither $KITSOKI_WEB_MAX_SESSIONS nor
// SetMaxSessions configures one explicitly. Picked generous enough that
// ordinary single-operator usage — a handful of story tabs open in a
// browser — never brushes against it, while still bounding the swarm-scale
// churn scenario (dozens of short-lived persona/UI-QA sessions started back
// to back) that would otherwise leak an orchestrator per session for the life
// of the process, per the package doc's now-revisited "no cap" lean.
const DefaultMaxLiveSessions = 128

// maxSessionsFromEnv resolves the configured cap from $KITSOKI_WEB_MAX_SESSIONS,
// falling back to DefaultMaxLiveSessions when unset or not a positive integer.
func maxSessionsFromEnv() int {
	v := os.Getenv("KITSOKI_WEB_MAX_SESSIONS")
	if v == "" {
		return DefaultMaxLiveSessions
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return DefaultMaxLiveSessions
	}
	return n
}

// SetMaxSessions overrides the live-session cap after construction — the
// injection point a future `kitsoki web --max-sessions` flag would call, and
// what tests use to exercise eviction without starting 128 real sessions. n<=0
// restores DefaultMaxLiveSessions rather than disabling the cap (a cap of zero
// or negative would either always-evict or panic the eviction search).
func (r *SessionRegistry) SetMaxSessions(n int) {
	if n <= 0 {
		n = DefaultMaxLiveSessions
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxSessions = n
}

// ErrNoEvictableSession is returned by NewSession/AttachExternal when the live
// -session cap is reached and every live session is mid-turn (turnsInFlight >
// 0) — there is nothing safe to evict. Returning this beats the alternatives:
// silently exceeding the cap (defeats the point of having one) or blocking
// until something finishes (an operator-facing hang with no visible cause).
var ErrNoEvictableSession = errors.New("kitsoki web: live-session cap reached and every session is mid-turn; no idle session is evictable")

// ensureCapacityLocked makes room for one more live session when the cap is
// already met: it picks the least-recently-active session with no turn in
// flight and evicts it. Caller MUST hold r.mu for the whole
// check-evict-then-insert sequence (both NewSession and AttachExternal do),
// so two concurrent session.new calls can't both observe "under cap" and both
// insert, exceeding it.
//
// The returned *entry (nil when no eviction was needed) is the victim; the
// caller must release it via cleanupEvicted AFTER unlocking r.mu (Close/sink
// I/O has no business running under the map lock).
func (r *SessionRegistry) ensureCapacityLocked() (*entry, error) {
	if len(r.sessions) < r.maxSessions {
		return nil, nil
	}
	var victimID string
	var victim *entry
	for id, e := range r.sessions {
		if atomic.LoadInt32(&e.turnsInFlight) > 0 {
			continue // never evict a session mid-turn
		}
		if victim == nil || e.lastActive.Before(victim.lastActive) {
			victimID, victim = id, e
		}
	}
	if victim == nil {
		return nil, ErrNoEvictableSession
	}
	delete(r.sessions, victimID)
	if r.currentSessionID == victimID {
		r.currentSessionID = ""
	}
	return victim, nil
}

// cleanupEvicted releases everything an evicted session held: its trace sink
// and its sessionRuntime (which owns the orchestrator, its store handles, and
// any agent/IDE connections — rt.Close's usual shutdown path). e is already
// unreachable from r.sessions by the time this runs (ensureCapacityLocked
// deleted it under the lock), so:
//
//   - a subsequent RPC against its id resolves via Get with ok=false, which
//     the server surface turns into a clear "unknown session_id" error — not a
//     hang or a panic (see server.resolve);
//   - its notification relay (registered on e.rt.Orch via
//     r.notifier.AttachSession at NewSession/AttachExternal time — the leak
//     named in the package doc) needs no separate UnregisterObserver call: the
//     relay is reachable only through that orchestrator's observer list, and
//     once e.rt.Close() runs and e is dropped here, nothing in the process
//     still references the orchestrator, so the relay (and the orchestrator
//     itself) become eligible for garbage collection together. It will never
//     fire again because nothing can drive e.sid to produce a background turn
//     for it to relay.
//
// Called after r.mu is released (Close/sink I/O has no business running under
// the map lock).
func (r *SessionRegistry) cleanupEvicted(e *entry) {
	if e == nil {
		return
	}
	if e.sink != nil {
		_ = e.sink.Close()
	}
	if e.rt != nil {
		e.rt.Close()
	}
}

// beginTurn marks id as mid-turn (turnsInFlight+1), protecting it from
// idle eviction for the duration of the call. No-op if id is no longer live
// (e.g. it raced an eviction — vanishingly unlikely since the caller can only
// reach beginTurn through a Driver obtained from a still-registered entry, but
// defensive rather than a nil-deref). Called by trackingDriver.
func (r *SessionRegistry) beginTurn(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.sessions[id]; ok {
		atomic.AddInt32(&e.turnsInFlight, 1)
	}
}

// endTurn clears the mid-turn mark and stamps lastActive to now — the signal
// ensureCapacityLocked ranks idle victims on. Called by trackingDriver via
// defer, so it runs whether the call succeeded or errored.
func (r *SessionRegistry) endTurn(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.sessions[id]; ok {
		atomic.AddInt32(&e.turnsInFlight, -1)
		e.lastActive = time.Now()
	}
}

func (r *SessionRegistry) finishTurn(id string) {
	r.endTurn(id)
	r.syncDaemonJob(id)
}

// trackingDriver wraps a live session's [server.Driver] so the registry can
// enforce swarm-session-cap eviction safely: it marks the session mid-turn
// (via beginTurn/endTurn) around every call that advances or could race the
// session's teardown, so ensureCapacityLocked never picks a busy session as a
// victim. Read-only / no-advance methods (View, IntentInfo, DefaultIntent,
// PatchWorld, the inbox listing methods) are promoted unchanged from the
// embedded Driver, matching lockingDriver's split between advancing and
// read-only calls.
//
// The optional Driver extensions (HarnessController, WorkLister, ChatShower,
// GitHubInboxSyncer) are forwarded explicitly, mirroring lockingDriver, so
// wrapping a session's driver for tracking introduces no feature regression —
// a Driver that doesn't implement one of these reports the same
// not-configured shape a read-only surface would.
type trackingDriver struct {
	server.Driver
	reg *SessionRegistry
	id  string
}

// newTrackingDriver wraps inner so its turn-advancing calls bump the
// registry's mid-turn / lastActive bookkeeping for session id.
func newTrackingDriver(reg *SessionRegistry, id string, inner server.Driver) server.Driver {
	return &trackingDriver{Driver: inner, reg: reg, id: id}
}

func (d *trackingDriver) Turn(ctx context.Context, input string) (*orchestrator.TurnOutcome, error) {
	d.reg.beginTurn(d.id)
	defer d.reg.finishTurn(d.id)
	return d.Driver.Turn(ctx, input)
}

func (d *trackingDriver) SubmitDirect(ctx context.Context, intent string, slots map[string]any) (*orchestrator.TurnOutcome, error) {
	d.reg.beginTurn(d.id)
	defer d.reg.finishTurn(d.id)
	return d.Driver.SubmitDirect(ctx, intent, slots)
}

func (d *trackingDriver) ContinueTurn(ctx context.Context, slots map[string]any) (*orchestrator.TurnOutcome, error) {
	d.reg.beginTurn(d.id)
	defer d.reg.finishTurn(d.id)
	return d.Driver.ContinueTurn(ctx, slots)
}

func (d *trackingDriver) AskOffPath(ctx context.Context, input string) (string, error) {
	d.reg.beginTurn(d.id)
	defer d.reg.finishTurn(d.id)
	return d.Driver.AskOffPath(ctx, input)
}

func (d *trackingDriver) Teleport(ctx context.Context, notificationID string) (*orchestrator.TurnOutcome, error) {
	d.reg.beginTurn(d.id)
	defer d.reg.finishTurn(d.id)
	return d.Driver.Teleport(ctx, notificationID)
}

func (d *trackingDriver) RewindRoute(ctx context.Context, decisionID string, newClass orchestrator.ContextRouteClass, reason string, workspacePath string) (*orchestrator.TurnOutcome, error) {
	d.reg.beginTurn(d.id)
	defer d.reg.finishTurn(d.id)
	return d.Driver.RewindRoute(ctx, decisionID, newClass, reason, workspacePath)
}

func (d *trackingDriver) HarnessProfiles() []orchestrator.ProfileInfo {
	if hc, ok := d.Driver.(server.HarnessController); ok {
		return hc.HarnessProfiles()
	}
	return nil
}

func (d *trackingDriver) HarnessSelection() orchestrator.ProfileSelection {
	if hc, ok := d.Driver.(server.HarnessController); ok {
		return hc.HarnessSelection()
	}
	return orchestrator.ProfileSelection{}
}

func (d *trackingDriver) SetHarnessSelection(profile, model, effort string) error {
	if hc, ok := d.Driver.(server.HarnessController); ok {
		return hc.SetHarnessSelection(profile, model, effort)
	}
	return nil
}

func (d *trackingDriver) CurrentWorld(ctx context.Context) (map[string]any, error) {
	if wr, ok := d.Driver.(server.WorldReader); ok {
		return wr.CurrentWorld(ctx)
	}
	return nil, fmt.Errorf("session driver exposes no world reader")
}

func (d *trackingDriver) ListWork(ctx context.Context) (server.SessionWork, error) {
	if wl, ok := d.Driver.(server.WorkLister); ok {
		return wl.ListWork(ctx)
	}
	return server.SessionWork{}, nil
}

func (d *trackingDriver) ShowChat(ctx context.Context, chatID string, sinceSeq int) (server.ChatShowResult, error) {
	if cs, ok := d.Driver.(server.ChatShower); ok {
		return cs.ShowChat(ctx, chatID, sinceSeq)
	}
	return server.ChatShowResult{}, fmt.Errorf("chat.show: no chat store configured")
}

func (d *trackingDriver) SyncGitHubInbox(ctx context.Context, opts server.GitHubInboxSyncOptions) (server.GitHubInboxSyncResult, error) {
	if sy, ok := d.Driver.(server.GitHubInboxSyncer); ok {
		return sy.SyncGitHubInbox(ctx, opts)
	}
	return server.GitHubInboxSyncResult{}, fmt.Errorf("inbox.sync_github: not supported")
}

func (r *SessionRegistry) implicitRootPath() (string, error) {
	repoRoot, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve implicit root cwd: %w", err)
	}
	return filepath.Join(repoRoot, ".kitsoki", "implicit-root.app.yaml"), nil
}

func (r *SessionRegistry) synthesizeImplicitRoot() (*storyLoad, error) {
	repoRoot, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve implicit root cwd: %w", err)
	}
	path := filepath.Join(repoRoot, ".kitsoki", "implicit-root.app.yaml")
	load := func() (*app.AppDef, error) {
		return app.SynthesizeRootWithResolver(r.cfg.Root.RootSpec(), repoRoot, buildImportResolver())
	}
	def, err := load()
	if err != nil {
		return nil, err
	}
	return &storyLoad{
		path:      path,
		def:       def,
		reloader:  load,
		synthetic: true,
		repoRoot:  repoRoot,
	}, nil
}

// synthesizeAgentRoot builds the storyLoad for an `agent:<name>` virtual
// story path. Like the implicit root, the synthesized AppDef has no file on
// disk: `synthetic` suppresses the on-disk staleness diff, and the injected
// reloader re-resolves the agent definition and re-synthesizes so
// runstatus.session.reload picks up an agent TOML / library edit.
//
// The synthesis-time write/external preflight runs against the registry's
// already-resolved launch policy (r.base.AgentLaunchPolicy — the `kitsoki web
// --config` file that also gates in-session dispatches), NOT the cwd
// .kitsoki.yaml the bare-CLI loadAgentSchemeApp head reads: a daemon's
// working directory is unrelated to the operator's configuration. Mirrors
// the MCP studio head (internal/mcp/studio/session_runtime.go).
func (r *SessionRegistry) synthesizeAgentRoot(storyPath string) (*storyLoad, error) {
	name, ok := agentroot.IsAgentPath(storyPath)
	if !ok {
		return nil, fmt.Errorf("not an agent story path: %q", storyPath)
	}
	repoRoot, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve agent root cwd: %w", err)
	}
	load := func() (*app.AppDef, error) {
		return loadAgentSchemeAppWithPolicy(name, r.base.AgentLaunchPolicy)
	}
	def, err := load()
	if err != nil {
		return nil, err
	}
	return &storyLoad{
		path:      storyPath,
		def:       def,
		reloader:  load,
		synthetic: true,
		repoRoot:  repoRoot,
	}, nil
}

func (r *SessionRegistry) loadStory(storyPath string) (*storyLoad, error) {
	// The `agent:<name>` scheme is a virtual story path — synthesize the agent
	// root before any path-shaped handling (parallel to the implicit-root
	// branch below). newSession, NewSessionSeeded, AttachExternal, and daemon
	// restore all flow through here, so every provider surface accepts
	// `runstatus.session.new {story_path: "agent:<name>"}` with zero RPC
	// contract change.
	if _, ok := agentroot.IsAgentPath(storyPath); ok {
		return r.synthesizeAgentRoot(storyPath)
	}
	abs, err := filepath.Abs(storyPath)
	if err != nil {
		return nil, fmt.Errorf("resolve story path %q: %w", storyPath, err)
	}
	implicit, impErr := r.implicitRootPath()
	if impErr != nil {
		return nil, impErr
	}
	if abs == implicit {
		return r.synthesizeImplicitRoot()
	}

	def, err := loadAppWithEnv(abs)
	if err != nil {
		return nil, err
	}
	rawContent, _ := os.ReadFile(abs)
	return &storyLoad{
		path:     abs,
		def:      def,
		raw:      rawContent,
		repoRoot: filepath.Dir(abs),
	}, nil
}

// SetNotifier injects the cross-session notification relay sink (the running
// server). It must be called before the first NewSession so every live session
// registers its relay. Idempotent-safe to call once at startup.
func (r *SessionRegistry) SetNotifier(n server.Notifier) {
	r.notifier = n
}

// Close releases every live session's runtime and sink, in arbitrary order. The
// `kitsoki web` entrypoint defers this on shutdown.
func (r *SessionRegistry) Close() {
	r.mu.Lock()
	services := make([]*campaign.Service, 0, len(r.campaignServices))
	for _, service := range r.campaignServices {
		services = append(services, service)
	}
	r.mu.Unlock()
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, service := range services {
		_ = service.Close(closeCtx)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.sessions {
		if e.sink != nil {
			_ = e.sink.Close()
		}
		if e.rt != nil {
			e.rt.Close()
		}
	}
	if r.metaSelfStr != nil {
		_ = r.metaSelfStr.Close()
	}
	if r.daemonStore != nil {
		_ = r.daemonStore.Close()
	}
}

// NewSession starts a fresh session for the story at storyPath, mirroring the
// single-session bootstrap web.go performed: build the runtime, create an
// orchestrator session, wire a concurrency-safe LiveSession event sink, record
// the effective story, and fire the initial on_enter chain so the first frame
// the browser renders reflects on-enter-bound world keys. It FAILS FAST with a
// structured error on an invalid story (app.Load / build / session errors) so
// the UI can surface it before navigating — no session is registered on failure.
func (r *SessionRegistry) NewSession(ctx context.Context, storyPath string) (string, error) {
	return r.newSession(ctx, storyPath, nil)
}

// NewSessionSeeded implements [server.SeededSessionProvider].
func (r *SessionRegistry) NewSessionSeeded(ctx context.Context, storyPath string, initialWorld map[string]any) (string, error) {
	return r.newSession(ctx, storyPath, initialWorld)
}

// NewRegisteredApplicationSession implements
// [server.RegisteredApplicationProvider]. Application IDs are resolved
// exactly and uniquely against the discovered story catalog before the
// internal path is used to create a live session.
func (r *SessionRegistry) NewRegisteredApplicationSession(ctx context.Context, applicationID string) (string, error) {
	r.mu.Lock()
	var matches []string
	for _, story := range r.stories {
		if story.Def != nil && story.Def.App.ID == applicationID {
			matches = append(matches, story.Path)
		}
	}
	r.mu.Unlock()
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("registered application %q not found", applicationID)
	case 1:
		return r.newSession(ctx, matches[0], nil)
	default:
		return "", fmt.Errorf("registered application %q is ambiguous (%d exact matches)", applicationID, len(matches))
	}
}

func (r *SessionRegistry) newSession(ctx context.Context, storyPath string, initialWorld map[string]any) (string, error) {
	return r.newSessionWithOrigin(ctx, storyPath, initialWorld, artifactjob.Origin{})
}

func (r *SessionRegistry) newSessionWithOrigin(
	ctx context.Context,
	storyPath string,
	initialWorld map[string]any,
	origin artifactjob.Origin,
) (string, error) {
	loaded, err := r.loadStory(storyPath)
	if err != nil {
		return "", err
	}
	abs := loaded.path
	def := loaded.def
	id := uuid.NewString()

	// Fail fast (never silently no-op a guarded turn): if the story gates a turn
	// on an author ACL but the server was started with no configured operator
	// identity, a browser-driven `continue` would record the anonymous fallback
	// and the guard would reject it. Surface that at session start instead.
	if err := r.checkAuthorIdentity(def); err != nil {
		return "", err
	}

	rtCfg := r.base.config(abs, def)
	if loaded.reloader != nil {
		rtCfg.Reloader = loaded.reloader
		rtCfg.MiningRepoPath = loaded.repoRoot
	}
	rt, err := buildSessionRuntime(rtCfg)
	if err != nil {
		return "", err
	}
	r.wireRunstatusSnapshot(rt, def.App.ID)
	r.wireFeedback(rt, def.App.ID, def.App.Author, def.App.Version)
	r.wireFeedbackReconciliation(rt, def.App.ID, def.App.Author, def.App.Version)
	r.wireCampaign(rt, def.App.ID)
	r.wireApplicationReadModels(rt, def.App.ID, loaded.path)
	if err := r.wireApplicationGraph(rt, def.App.ID, loaded.path); err != nil {
		rt.Close()
		return "", err
	}
	r.wireFlowEvidence(rt, def.App.ID, def.App.Author, def.App.Version)
	r.wireApplicationJob(rt, def.App.ID)
	if err := r.wireApplicationConversation(rt, def.App.ID); err != nil {
		return "", err
	}
	// On any error after construction, release what we opened so a failed
	// NewSession leaks nothing.
	ok := false
	defer func() {
		if !ok {
			rt.Close()
		}
	}()

	orch := rt.Orch
	sid, err := orch.NewSession(ctx)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	if r.daemonJobs != nil {
		if err := rt.Store.BindExternalKey(ctx, sid, "daemon", id); err != nil {
			_ = rt.Store.MarkAbandoned(ctx, sid)
			return "", fmt.Errorf("bind daemon job %q: %w", id, err)
		}
	}

	// In flow posture (--flow / --host-cassette), honor the fixture's
	// initial_state / initial_world exactly as `test flows` and `record` do:
	// teleport the freshly created session onto the fixture's starting state and
	// seed its world keys before any frame is observed. Gated on a fixture being
	// present so default (no-flow) web sessions still start at the app root.
	// Returns the effective initial state so the LiveSession + the first frame
	// reflect the seed (it is orch.InitialState() when no seed applied).
	seedFixture := r.base.Flow
	if seedFixture == nil {
		// Live-harness replay posture: no flow fixture, but a seed-only fixture
		// may still teleport the session onto a mid-graph start state + world.
		seedFixture = r.base.SeedFixture
	}
	if len(initialWorld) > 0 {
		seedFixture = mergeSeedFixture(seedFixture, initialWorld)
	}
	initialState, err := seedFlowInitialState(orch, rt.Store, sid, seedFixture)
	if err != nil {
		return "", fmt.Errorf("seed flow initial state: %w", err)
	}

	traceTransport, traceThread := "web", string(sid)
	if r.daemonJobs != nil {
		traceTransport, traceThread = "daemon", id
	}
	tracePath := store.DefaultTracePath(def.App.ID, traceTransport, traceThread)
	if mkErr := os.MkdirAll(filepath.Dir(tracePath), 0o755); mkErr != nil {
		return "", fmt.Errorf("create trace directory: %w", mkErr)
	}
	sink, err := store.OpenJSONL(tracePath)
	if err != nil {
		return "", fmt.Errorf("open trace sink: %w", err)
	}
	sinkOK := false
	defer func() {
		if !sinkOK {
			_ = sink.Close()
		}
	}()

	// Wrap the sink so the orchestrator's appends and the HTTP server's reads
	// share one lock — the JSONLSink underneath is not safe for concurrent
	// Append + History (mirrors web.go).
	live := server.NewLiveSession(sink, def, string(sid), string(initialState))
	orch.SetEventSink(live)
	// Bind the cassette deferred agent sink now that the real sink is ready.
	if rt.DeferredAgentSink != nil {
		rt.DeferredAgentSink.SetSink(live)
	}

	daemonRegistered := false
	if r.daemonJobs != nil {
		if origin.Kind == "" {
			origin = artifactjob.Origin{Kind: "daemon", Ref: "daemon:" + id}
		}
		_, err = r.daemonJobs.Register(ctx, artifactjob.RegisterRequest{
			ID:         artifactjob.JobID(id),
			SessionID:  sid,
			AppID:      def.App.ID,
			Story:      abs,
			Origin:     origin,
			Status:     artifactjob.StatusRunning,
			RunURL:     "/s/" + id,
			TracePath:  tracePath,
			Summary:    storyTitle(def),
			Phase:      string(initialState),
			Visibility: artifactjob.VisibilityLocal,
			Owner:      r.base.DefaultActor,
		})
		if err != nil {
			_ = rt.Store.MarkAbandoned(ctx, sid)
			return "", fmt.Errorf("register daemon job: %w", err)
		}
		daemonRegistered = true
		defer func() {
			if ok || !daemonRegistered {
				return
			}
			status := artifactjob.StatusFailed
			summary := "daemon session initialization failed"
			_, _ = r.daemonJobs.Update(context.Background(), artifactjob.JobID(id), artifactjob.Update{Status: &status, Summary: &summary})
		}()
	}

	// Record the effective story as the first event so the trace self-describes
	// even after a later hot-reload (matches `kitsoki run` and web.go).
	if err := orch.RecordEffectiveStory(ctx, sid); err != nil {
		return "", fmt.Errorf("record effective story: %w", err)
	}

	// Fire the initial state's on_enter chain before the browser observes the
	// session so the first frame reflects on-enter-bound world keys (web.go).
	if err := orch.RunInitialOnEnter(ctx, sid); err != nil {
		return "", fmt.Errorf("run initial on_enter: %w", err)
	}

	e := &entry{
		StoryPath:     abs,
		Def:           def,
		synthetic:     loaded.synthetic,
		loadedContent: loaded.raw,
		rt:            rt,
		sid:           sid,
		sessionDir:    filepath.Dir(tracePath),
		source:        live,
		driver:        server.OrchestratorDriver{Orch: orch, SID: sid, Jobs: rt.JobStore, Chats: rt.ChatStore, TraceHistory: live.History},
		sink:          sink,
		lastActive:    time.Now(),
	}
	e.driver = newTrackingDriver(r, id, e.driver)

	// Register a per-session notification relay so the orchestrator's
	// background-turn fan-out reaches the cross-session SSE feed. The relay is
	// never explicitly unregistered here — it lives as long as the
	// orchestrator does. That used to mean "as long as the process" (the
	// original PoC leak this package doc named), but sessions are now bounded
	// (swarm-session-cap): once ensureCapacityLocked evicts this entry and
	// cleanupEvicted closes rt, the orchestrator (and the relay registered on
	// it) become unreachable and are collected together — see cleanupEvicted's
	// doc. The notifier is injected by web.go via SetNotifier after the server
	// is constructed; it is nil in tests that build a registry without a
	// server.
	if r.notifier != nil {
		r.notifier.AttachSession(orch, sid, id, rt.JobStore)
	}

	r.mu.Lock()
	victim, evictErr := r.ensureCapacityLocked()
	if evictErr != nil {
		r.mu.Unlock()
		return "", evictErr
	}
	r.sessions[id] = e
	r.currentSessionID = id
	r.mu.Unlock()
	r.cleanupEvicted(victim)
	r.syncDaemonJob(id)

	// The current session changed: notify subscribers (trace-only / graph-only
	// surfaces follow this). Emitted outside the lock, after the value is
	// committed. Nil in tests that build a registry without a server.
	if r.notifier != nil {
		r.notifier.EmitCurrentSession(id, true)
	}

	ok = true
	sinkOK = true
	return id, nil
}

func mergeSeedFixture(base *testrunner.FlowFixture, initialWorld map[string]any) *testrunner.FlowFixture {
	out := &testrunner.FlowFixture{}
	if base != nil {
		out.InitialState = base.InitialState
		if len(base.InitialWorld) > 0 {
			out.InitialWorld = make(map[string]any, len(base.InitialWorld)+len(initialWorld))
			for k, v := range base.InitialWorld {
				out.InitialWorld[k] = v
			}
		}
	}
	if out.InitialWorld == nil {
		out.InitialWorld = make(map[string]any, len(initialWorld))
	}
	for k, v := range initialWorld {
		out.InitialWorld[k] = v
	}
	return out
}

// AttachExternal implements [server.ExternalAttachProvider]: it binds a live web
// session to a session in the PERSISTED store addressed by an external key
// (`transport:thread`). If a session is already bound to that key it attaches to
// it (the browser co-drives the same session a `kitsoki session continue` /
// loop.py process drives); otherwise it creates a session and binds the key. The
// returned session is driven under the per-session writer lock, so concurrent
// drivers (browser, inbound bridge, a separate continue process) serialise
// rather than interleave — a loser gets store.ErrSessionBusy (EX_TEMPFAIL) and
// retries, never a corrupted journey.
//
// Live SSE reflects turns this process drives. A turn another process commits is
// visible after a session reload (it is read from the shared store), not pushed
// over SSE — the exclusive trace-file flock means two processes cannot share one
// live trace stream. That cross-process live-stream is the remaining engine work
// noted in docs/architecture/transports.md.
func (r *SessionRegistry) AttachExternal(ctx context.Context, storyPath, key string) (string, error) {
	transportID, thread, err := parseExternalKey(key)
	if err != nil {
		return "", err
	}

	// Attaching the same external key twice in one process must return the live
	// session already bound to it, not open a second trace sink (the trace file
	// flock is exclusive — a second open would fail) and not split one ticket
	// across two in-process sessions.
	if id, found := r.liveByExternalKey(key); found {
		// Re-attaching to an already-live session makes it the current session
		// again (an operator opening that ticket's surface is now following it).
		r.mu.Lock()
		r.currentSessionID = id
		r.mu.Unlock()
		if r.notifier != nil {
			r.notifier.EmitCurrentSession(id, true)
		}
		return id, nil
	}

	loaded, err := r.loadStory(storyPath)
	if err != nil {
		return "", err
	}
	abs := loaded.path
	def := loaded.def
	if err := r.checkAuthorIdentity(def); err != nil {
		return "", err
	}

	rtCfg := r.base.config(abs, def)
	if loaded.reloader != nil {
		rtCfg.Reloader = loaded.reloader
		rtCfg.MiningRepoPath = loaded.repoRoot
	}
	rt, err := buildSessionRuntime(rtCfg)
	if err != nil {
		return "", err
	}
	r.wireRunstatusSnapshot(rt, def.App.ID)
	r.wireFeedback(rt, def.App.ID, def.App.Author, def.App.Version)
	r.wireFeedbackReconciliation(rt, def.App.ID, def.App.Author, def.App.Version)
	r.wireCampaign(rt, def.App.ID)
	r.wireApplicationReadModels(rt, def.App.ID, loaded.path)
	if err := r.wireApplicationGraph(rt, def.App.ID, loaded.path); err != nil {
		rt.Close()
		return "", err
	}
	r.wireFlowEvidence(rt, def.App.ID, def.App.Author, def.App.Version)
	r.wireApplicationJob(rt, def.App.ID)
	if err := r.wireApplicationConversation(rt, def.App.ID); err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			rt.Close()
		}
	}()

	orch := rt.Orch

	// Resolve the external key to a persisted session, or create+bind one. Both
	// the lookup and the create run against the shared store the persisted
	// drivers use.
	sid, lookErr := rt.Store.LookupByKey(ctx, transportID, thread)
	created := false
	switch {
	case lookErr == nil:
		// Attach to the existing persisted session — nothing to create.
	case errors.Is(lookErr, store.ErrSessionNotFound):
		sid, err = orch.NewSession(ctx)
		if err != nil {
			return "", fmt.Errorf("create session: %w", err)
		}
		if err := rt.Store.BindExternalKey(ctx, sid, transportID, thread); err != nil {
			return "", fmt.Errorf("bind external key %q: %w", key, err)
		}
		created = true
	default:
		return "", fmt.Errorf("lookup external key %q: %w", key, lookErr)
	}

	// Open a live trace over the deterministic per-(app,transport,thread) path so
	// any history a prior in-process run wrote is loaded and the browser sees it.
	tracePath := store.DefaultTracePath(def.App.ID, transportID, thread)
	if mkErr := os.MkdirAll(filepath.Dir(tracePath), 0o755); mkErr != nil {
		return "", fmt.Errorf("create trace directory: %w", mkErr)
	}
	sink, err := store.OpenJSONL(tracePath)
	if err != nil {
		return "", fmt.Errorf("open trace sink: %w", err)
	}
	sinkOK := false
	defer func() {
		if !sinkOK {
			_ = sink.Close()
		}
	}()

	live := server.NewLiveSession(sink, def, string(sid), string(orch.InitialState()))
	orch.SetEventSink(live)
	if rt.DeferredAgentSink != nil {
		rt.DeferredAgentSink.SetSink(live)
	}

	if err := orch.RecordEffectiveStory(ctx, sid); err != nil {
		return "", fmt.Errorf("record effective story: %w", err)
	}
	// Fire the initial on_enter only for a freshly-created session; an existing
	// persisted session is already past its initial frame and re-firing would
	// re-run on_enter effects against a mid-flight world.
	if created {
		if err := orch.RunInitialOnEnter(ctx, sid); err != nil {
			return "", fmt.Errorf("run initial on_enter: %w", err)
		}
	}

	// The driver advances the session under the store's per-session writer lock,
	// so co-driving (browser + bridge + a continue process) serialises.
	lockedSID := sid
	lock := func(lctx context.Context, fn func() error) error {
		return rt.Store.WithWriterLock(lctx, lockedSID, fn)
	}
	driver := server.NewLockingDriver(server.OrchestratorDriver{Orch: orch, SID: sid, Jobs: rt.JobStore, Chats: rt.ChatStore, TraceHistory: live.History}, lock)

	id := uuid.NewString()
	if transportID == "daemon" && thread != "" {
		id = thread
	}
	e := &entry{
		StoryPath:     abs,
		Def:           def,
		synthetic:     loaded.synthetic,
		externalKey:   key,
		loadedContent: loaded.raw,
		rt:            rt,
		sid:           sid,
		sessionDir:    filepath.Dir(tracePath),
		source:        live,
		driver:        driver,
		sink:          sink,
		lastActive:    time.Now(),
	}
	e.driver = newTrackingDriver(r, id, e.driver)

	r.mu.Lock()
	victim, evictErr := r.ensureCapacityLocked()
	if evictErr != nil {
		r.mu.Unlock()
		return "", evictErr
	}
	r.sessions[id] = e
	r.currentSessionID = id
	r.mu.Unlock()
	r.cleanupEvicted(victim)

	// The current session changed: notify subscribers (same as NewSession).
	if r.notifier != nil {
		r.notifier.EmitCurrentSession(id, true)
	}

	// Attach the cross-session notification relay, same as NewSession — see
	// the note there for why eviction needs no matching UnregisterObserver.
	if r.notifier != nil {
		r.notifier.AttachSession(orch, sid, id, rt.JobStore)
	}

	ok = true
	sinkOK = true
	return id, nil
}

// liveByExternalKey returns the id of a live session already bound to key, if
// any. Used so a re-attach to the same ticket reuses the live session.
func (r *SessionRegistry) liveByExternalKey(key string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, e := range r.sessions {
		if e.externalKey == key {
			return id, true
		}
	}
	return "", false
}

// checkAuthorIdentity enforces the operator-identity invariant: a story that
// reads an author ACL key in a guard (today none ship one — stories/bugfix
// declares `allowed_authors` but never reads it) requires a configured server
// identity, because a browser turn with no identity would record the anonymous
// fallback and bounce off the guard with no operator-facing reason. The
// per-request X-Kitsoki-Actor header / actor RPC param are NOT "configured" —
// they are optional and may be absent on any turn — so the only thing that
// satisfies the invariant at start time is a server-level default actor.
func (r *SessionRegistry) checkAuthorIdentity(def *app.AppDef) error {
	if r.base.DefaultActor != "" {
		return nil
	}
	if def.ReadsWorldKeyInGuard("allowed_authors") {
		return fmt.Errorf(
			"story %q gates a turn on the author ACL key 'allowed_authors' but the web server has no configured operator identity; start with --actor <name> so browser-driven turns record a real principal",
			storyTitle(def))
	}
	return nil
}

// Get resolves a live session id to the server.Entry the server routes against.
// ok is false for an unknown id; the server turns that into a structured
// not-found error rather than a nil-deref.
func (r *SessionRegistry) Get(sessionID string) (server.Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.sessions[sessionID]
	if !ok {
		return server.Entry{}, false
	}
	return server.Entry{
		Source:    e.source,
		Driver:    e.driver,
		Meta:      &metaDriver{ctrl: r.metaControllerForLocked(e), chats: e.rt.ChatStore, entry: e},
		Artifacts: &server.JournalArtifactResolver{Reader: e.rt.JournalRead, SID: e.sid},
		Frames:    e.frameRecorderLocked(),
		Feedback:  e.feedbackSinkLocked(),
		Stream:    entrySessionStream(e),
	}, true
}

// entrySessionStream exposes the durable-stream read capability of e's
// session store when the backend provides one (postgres/embedded-postgres via
// store.AsEventStream); nil on SQLite, which keeps the server on its existing
// Source-polling SSE path and makes runstatus.session.events report its typed
// not-supported error. The store session id rides along because the registry's
// public session id (the RPC session_id) is not the store's.
func entrySessionStream(e *entry) *server.SessionStream {
	if e.rt == nil || e.rt.Store == nil {
		return nil
	}
	es, ok := store.AsEventStream(e.rt.Store)
	if !ok {
		return nil
	}
	return &server.SessionStream{Stream: es, SID: e.sid}
}

// ApplicationEventScheduler returns the durable SQLite-backed scheduler
// already owned by the live session runtime.
func (r *SessionRegistry) ApplicationEventScheduler(sessionID string) (jobs.Scheduler, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.sessions[sessionID]
	if !ok || e.rt == nil || e.rt.Scheduler == nil {
		return nil, false
	}
	return e.rt.Scheduler, true
}

func (r *SessionRegistry) ApplicationHostRegistry(sessionID string) (*host.Registry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.sessions[sessionID]
	if !ok || e.rt == nil || e.rt.HostRegistry == nil {
		return nil, false
	}
	return e.rt.HostRegistry, true
}

func (r *SessionRegistry) wireRunstatusSnapshot(rt *sessionRuntime, appID string) {
	if r.daemonJobs == nil || rt == nil || rt.HostRegistry == nil {
		return
	}
	rt.HostRegistry.Replace(
		"host.runstatus",
		host.NewRunstatusSnapshotHandler(r.daemonJobs, appID),
	)
}

func (r *SessionRegistry) wireApplicationReadModels(rt *sessionRuntime, appID, appPath string) {
	if r.daemonJobs == nil || rt == nil || rt.HostRegistry == nil {
		return
	}
	binding, ok := r.cfg.ApplicationReadModels[appID]
	if !ok {
		return
	}
	projectRoot := compliance.DiscoverRoot(appPath)
	if binding.Streams != nil {
		rt.HostRegistry.Replace(
			"host.streams",
			host.NewStreamsSnapshotHandler(
				queueDeliveryStreamSource{
					store:     queue.Store{ProjectRoot: projectRoot},
					projectID: binding.Streams.Scope,
				},
				appID,
				binding.Streams.Scope,
			),
		)
	}
	if binding.Federation {
		entries := append([]workerregistry.Entry(nil), r.cfg.Workers...)
		if len(entries) == 0 {
			entries = workerregistry.FromDaemonFederation(r.cfg.DaemonFederation)
		}
		rt.HostRegistry.Replace(
			"host.federation",
			host.NewFederationSnapshotHandler(
				registryFederationSource{
					entries: entries,
					pool:    r.federation,
					policy:  r.base.AgentLaunchPolicy.Placement,
				},
				appID,
			),
		)
	}
	if binding.Materialization && r.materializations != nil {
		rt.HostRegistry.Replace(
			"host.materialization",
			host.NewMaterializationSnapshotHandler(r.materializations, appID),
		)
	}
}

func (r *SessionRegistry) MaterializationProjection() (materializationstatus.Store, string, bool) {
	if r.materializations == nil {
		return nil, "", false
	}
	owner, ok := r.configuredMaterializationApplication()
	if !ok {
		return nil, "", false
	}
	return r.materializations, owner, true
}

func (r *SessionRegistry) configuredMaterializationApplication() (string, bool) {
	owner := ""
	for appID, binding := range r.cfg.ApplicationReadModels {
		if !binding.Materialization {
			continue
		}
		if owner != "" {
			return "", false
		}
		owner = appID
	}
	if owner == "" {
		return "", false
	}
	return owner, true
}

type queueDeliveryStreamSource struct {
	store     queue.Store
	projectID string
}

func (s queueDeliveryStreamSource) ListDeliveryStreams(_ context.Context, limit int) ([]host.DeliveryStreamRecord, error) {
	if err := rejectQueueSymlinks(s.store.ProjectRoot); err != nil {
		return nil, err
	}
	state, err := s.store.List()
	if err != nil {
		return nil, err
	}
	out := make([]host.DeliveryStreamRecord, 0, min(limit, len(state.Candidates)))
	for _, candidate := range state.Candidates {
		if !strings.EqualFold(strings.TrimSpace(candidate.ProjectID), strings.TrimSpace(s.projectID)) {
			continue
		}
		head := strings.TrimSpace(candidate.ValidatedSHA)
		if head == "" {
			head = strings.TrimSpace(candidate.SHA)
		}
		out = append(out, host.DeliveryStreamRecord{
			ID:            candidate.ID,
			Lane:          string(candidate.TargetPolicy),
			State:         string(candidate.Status),
			ImmutableHead: head,
			TargetRef:     candidate.TargetRef,
			ProposalState: queueProposalState(candidate),
			GateStatus:    queueGateStatus(candidate.Status),
			NextAction:    queueNextAction(candidate.Status),
			ReceiptRef:    candidate.ReceiptID,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func rejectQueueSymlinks(root string) error {
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return fmt.Errorf("resolve configured project root: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect configured project root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("configured project root must not be a symlink")
	}
	for _, path := range []string{
		filepath.Join(root, ".capsules"),
		filepath.Join(root, ".capsules", "queue"),
		filepath.Join(root, ".capsules", "queue", "state.json"),
	} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("delivery stream store contains a symlink")
		}
	}
	return nil
}

func queueProposalState(candidate queue.Candidate) string {
	switch candidate.Status {
	case queue.AwaitingApproval:
		return "awaiting-approval"
	case queue.Rejected:
		return "rejected"
	default:
		if candidate.ReceiptID != "" {
			return "receipt-admitted"
		}
		return "unverified"
	}
}

func queueGateStatus(status queue.Status) string {
	switch status {
	case queue.ReadyToFinalize, queue.Finalizing, queue.Landed:
		return "passed"
	case queue.NeedsInput, queue.NeedsConflictInput, queue.Rejected:
		return "failed"
	case queue.Gating:
		return "running"
	default:
		return "pending"
	}
}

func queueNextAction(status queue.Status) string {
	switch status {
	case queue.AwaitingApproval:
		return "approve"
	case queue.ReadyToFinalize:
		return "finalize"
	case queue.NeedsInput, queue.NeedsConflictInput, queue.Rejected:
		return "review"
	case queue.RetryWait:
		return "retry"
	case queue.Landed:
		return "none"
	default:
		return "advance"
	}
}

type registryFederationSource struct {
	entries []workerregistry.Entry
	pool    *daemonfederation.Pool
	policy  map[string]host.PlacementLanePolicy
}

func (s registryFederationSource) FederationSnapshot(ctx context.Context) (host.FederationProjection, error) {
	health := map[string]daemonfederation.WorkerStatus{}
	if s.pool != nil {
		for _, worker := range s.pool.Get(ctx).Workers {
			health[worker.ID] = worker
		}
	}
	projection := host.FederationProjection{
		Workers: make([]host.FederationWorker, 0, len(s.entries)),
		Policy:  make([]host.FederationPolicy, 0, len(s.policy)),
	}
	for _, entry := range s.entries {
		status := health[entry.ID]
		healthName := status.Health
		if healthName == "" {
			healthName = "unknown"
		}
		projection.Workers = append(projection.Workers, host.FederationWorker{
			ID: entry.ID, Label: entry.Label, Placement: entry.Placement,
			Health: healthName, Enabled: entry.Enabled, Jobs: status.JobCount,
			Capabilities: host.FederationCapabilities{
				Placements: append([]string(nil), entry.Capabilities.Placements...),
				Isolation:  entry.Capabilities.Isolation,
				Networks:   append([]string(nil), entry.Capabilities.Networks...),
			},
		})
	}
	for lane, policy := range s.policy {
		projection.Policy = append(projection.Policy, host.FederationPolicy{
			Lane: lane, WorkerClasses: append([]string(nil), policy.WorkerClasses...),
			NetworkProfiles: append([]string(nil), policy.NetworkProfiles...),
		})
	}
	return projection, nil
}

func (r *SessionRegistry) wireFeedback(rt *sessionRuntime, appID, owner, revision string) {
	if rt == nil || rt.HostRegistry == nil {
		return
	}
	r.mu.Lock()
	backend := r.feedbackBackends[appID]
	r.mu.Unlock()
	if backend == nil {
		return
	}
	rt.HostRegistry.Replace(
		"host.feedback",
		host.NewFeedbackHandler(backend, host.FeedbackScope{
			ApplicationID: appID,
			Owner:         owner,
			Revision:      revision,
		}),
	)
}

func (r *SessionRegistry) wireFeedbackReconciliation(
	rt *sessionRuntime,
	appID, owner, revision string,
) {
	if rt == nil || rt.HostRegistry == nil || appID == "" {
		return
	}
	scope := host.FeedbackScope{ApplicationID: appID, Owner: owner, Revision: revision}
	r.mu.Lock()
	campaignBackend := r.feedbackBackends[appID]
	federationBackend := r.feedbackFederationBackends[appID]
	intakeBinding, intakeConfigured := r.cfg.FeedbackIntake[appID]
	intakeSource := r.feedbackCaptureSources[intakeBinding.Source]
	campaignBinding, campaignConfigured := r.cfg.ReviewedFeedback[appID]
	federationBinding, federationConfigured := r.cfg.FeedbackFederation[appID]
	store := r.feedbackReconciles
	ledger := r.feedbackLedger
	r.mu.Unlock()

	if campaignConfigured {
		service := reviewedfeedback.DispatchReconciler{
			Operation: reviewedfeedback.OperationCampaign,
			ConfigurationID: reviewedfeedback.BindingConfigurationID(
				campaignBinding.TargetApplication,
				campaignBinding.TargetHandler,
				campaignBinding.TargetAction,
			),
			Backend: campaignBackend, Store: store, Scope: scope,
			Limit: reviewedfeedback.DefaultDrainLimit,
		}
		rt.HostRegistry.Replace(
			host.ReviewedFeedbackCampaignReconcileVerb,
			host.NewFeedbackReconcileHandler(
				host.ReviewedFeedbackCampaignReconcileVerb,
				func(ctx context.Context) (host.Result, error) {
					result, err := service.Reconcile(ctx)
					return result.HostResult(), err
				},
			),
		)
	}
	if intakeConfigured {
		service := reviewedfeedback.IntakeReconciler{
			SourceID: intakeBinding.Source, Source: intakeSource,
			Ledger: ledger, Store: store, Scope: scope,
			Clock: clock.Real(), Limit: intakeBinding.MaxRecords,
		}
		rt.HostRegistry.Replace(
			host.FeedbackIntakeReconcileVerb,
			host.NewFeedbackReconcileHandler(
				host.FeedbackIntakeReconcileVerb,
				func(ctx context.Context) (host.Result, error) {
					result, err := service.Reconcile(ctx)
					return result.HostResult(), err
				},
			),
		)
	}
	if federationConfigured {
		service := reviewedfeedback.DispatchReconciler{
			Operation: reviewedfeedback.OperationFederation,
			ConfigurationID: reviewedfeedback.BindingConfigurationID(
				federationBinding.TargetApplication,
				federationBinding.TargetHandler,
				federationBinding.TargetAction,
			),
			Backend: federationBackend, Store: store, Scope: scope,
			Limit: federationBinding.MaxRecords,
		}
		rt.HostRegistry.Replace(
			host.FeedbackFederationReconcileVerb,
			host.NewFeedbackReconcileHandler(
				host.FeedbackFederationReconcileVerb,
				func(ctx context.Context) (host.Result, error) {
					result, err := service.Reconcile(ctx)
					return result.HostResult(), err
				},
			),
		)
	}
}

func (r *SessionRegistry) wireCampaign(rt *sessionRuntime, appID string) {
	if r.campaignStore == nil || r.campaignSource == nil || r.campaignScheduler == nil ||
		rt == nil || rt.HostRegistry == nil || appID == "" {
		return
	}
	r.mu.Lock()
	service := r.campaignServices[appID]
	if service == nil {
		service = &campaign.Service{
			Store:      r.campaignStore,
			Source:     r.campaignSource,
			Scheduler:  campaignSchedulerAdapter{scheduler: r.campaignScheduler},
			Clock:      clock.Real(),
			Dispatcher: campaignRegistryDispatcher{registry: r},
		}
		r.campaignServices[appID] = service
	}
	r.mu.Unlock()
	rt.HostRegistry.Replace("host.campaign", host.NewCampaignHandler(service, appID))
}

type campaignSchedulerAdapter struct {
	scheduler jobs.Scheduler
}

func (a campaignSchedulerAdapter) Submit(
	ctx context.Context,
	kind string,
	run func(context.Context) error,
) (string, error) {
	if a.scheduler == nil {
		return "", fmt.Errorf("campaign scheduler is unavailable")
	}
	return a.scheduler.Submit(ctx, jobs.JobSpec{
		Kind: kind,
		Handler: func(handlerCtx context.Context, _ map[string]any) (host.Result, error) {
			return host.Result{}, run(handlerCtx)
		},
	})
}

func (a campaignSchedulerAdapter) Cancel(ctx context.Context, ref string) error {
	if a.scheduler == nil {
		return nil
	}
	err := a.scheduler.Cancel(ctx, ref)
	if errors.Is(err, jobs.ErrJobNotFound) {
		return nil
	}
	return err
}

func (a campaignSchedulerAdapter) WaitIdle(ctx context.Context) error {
	if a.scheduler == nil {
		return nil
	}
	return a.scheduler.WaitIdle(ctx)
}

type campaignRegistryDispatcher struct {
	registry *SessionRegistry
}

func (d campaignRegistryDispatcher) Dispatch(ctx context.Context, claim campaign.Claim) (string, error) {
	if d.registry == nil {
		return "", fmt.Errorf("campaign dispatcher is unavailable")
	}
	jobRef, err := d.registry.newSessionWithOrigin(
		ctx,
		claim.Action.Story,
		nil,
		artifactjob.Origin{
			Kind: "campaign",
			Ref:  "campaign:" + claim.AppID + "/" + claim.ID + "/" + claim.IdempotencyKey,
		},
	)
	if err != nil {
		return "", err
	}
	d.registry.mu.Lock()
	entry := d.registry.sessions[jobRef]
	d.registry.mu.Unlock()
	if entry == nil || entry.driver == nil {
		return jobRef, fmt.Errorf("campaign dispatch %q has no live session driver", jobRef)
	}
	input := make(map[string]any, len(claim.Action.Input)+1)
	for key, value := range claim.Action.Input {
		input[key] = value
	}
	input["request_id"] = claim.IdempotencyKey
	if _, err := entry.driver.SubmitDirect(ctx, claim.Action.Intent, input); err != nil {
		return jobRef, err
	}
	return jobRef, nil
}

func (r *SessionRegistry) wireFlowEvidence(rt *sessionRuntime, appID, owner, revision string) {
	if rt == nil || rt.HostRegistry == nil {
		return
	}
	r.mu.Lock()
	provider, ok := r.flowEvidenceProviders[appID]
	r.mu.Unlock()
	if !ok {
		return
	}
	rt.HostRegistry.Replace(
		"host.flow_evidence",
		host.NewFlowEvidenceHandler(provider, host.FlowEvidenceScope{
			ApplicationID: appID,
			Owner:         owner,
			Revision:      revision,
			CatalogPath:   provider.CatalogPath,
		}),
	)
}

// CurrentSession implements [server.CurrentSessionProvider]: it returns the id of
// the most recently created (NewSession) or attached (AttachExternal) session.
// Trace-only and graph-only surfaces, which have no chat to start a session, read
// this to discover and follow the active session. ok is false when no session has
// been created yet, or when the tracked id is no longer live (defensive — the PoC
// never deletes entries).
func (r *SessionRegistry) CurrentSession() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentSessionID == "" {
		return "", false
	}
	if _, ok := r.sessions[r.currentSessionID]; !ok {
		return "", false
	}
	return r.currentSessionID, true
}

// List returns a runstatus.SessionHeader per live session, for
// runstatus.sessions.list. The header is read from each session's live snapshot
// so the current state / turn reflect where the session actually is.
func (r *SessionRegistry) List() []runstatus.SessionHeader {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]runstatus.SessionHeader, 0, len(r.sessions))
	for id, e := range r.sessions {
		snap, err := e.source.Snapshot()
		if err != nil {
			continue
		}
		// The header's SessionID must be the registry's entry key (the UUID
		// NewSession returned and Get routes on), NOT the orchestrator's
		// internal session id the snapshot carries — otherwise the home
		// screen's Open link / auto-nav would push a non-routable id.
		hdr := snap.Session
		hdr.SessionID = id
		out = append(out, hdr)
	}
	return out
}

// ListArtifactJobs implements server.ArtifactJobProvider. The durable list is
// intentionally independent of the live-session map, so completed and
// interrupted work remains visible after process teardown.
func (r *SessionRegistry) ListArtifactJobs(ctx context.Context) ([]server.ArtifactJobSummary, error) {
	out := []server.ArtifactJobSummary{}
	if r.daemonJobs != nil {
		jobs, err := r.daemonJobs.List(ctx, artifactjob.ListFilter{Limit: 200})
		if err != nil {
			return nil, err
		}
		out = make([]server.ArtifactJobSummary, 0, len(jobs))
		for _, job := range jobs {
			story := job.Story
			sessionID := string(job.SessionID)
			runURL := job.RunURL
			openURL := job.RunURL
			if job.Origin.Kind == "application-artifact" || job.Origin.Kind == "application-job" {
				story = "application:" + job.AppID
				sessionID = ""
				runURL = ""
				openURL = ""
			}
			out = append(out, server.ArtifactJobSummary{
				JobID:             string(job.ID),
				SessionID:         sessionID,
				AppID:             job.AppID,
				Story:             story,
				Status:            string(job.Status),
				Phase:             job.Phase,
				Summary:           job.Summary,
				RunURL:            runURL,
				UpdatedAt:         job.UpdatedAt,
				InterruptedReason: job.InterruptedReason,
				WorkerID:          "local",
				WorkerLabel:       "Local",
				Placement:         "local",
				OpenURL:           openURL,
			})
		}
	}
	if r.federation != nil {
		snapshot := r.federation.Get(ctx)
		for _, job := range snapshot.Jobs {
			out = append(out, server.ArtifactJobSummary{
				JobID: job.JobID, SessionID: job.SessionID, AppID: job.AppID,
				Story: job.Story, Status: job.Status, Phase: job.Phase,
				Summary: job.Summary, RunURL: job.RunURL, UpdatedAt: job.UpdatedAt,
				InterruptedReason: job.InterruptedReason, WorkerID: job.WorkerID,
				WorkerLabel: job.WorkerLabel, Placement: job.Placement, OpenURL: job.OpenURL,
			})
		}
	}
	return out, nil
}

func (r *SessionRegistry) ListWorkers(ctx context.Context) ([]server.WorkerSummary, error) {
	if r.daemonJobs == nil && r.federation == nil {
		return []server.WorkerSummary{}, nil
	}
	localJobs := 0
	if r.daemonJobs != nil {
		jobs, err := r.daemonJobs.List(ctx, artifactjob.ListFilter{Limit: 200})
		if err != nil {
			return nil, err
		}
		localJobs = len(jobs)
	}
	out := []server.WorkerSummary{{ID: "local", Label: "Local", Placement: "local", Health: "online", LastSeen: time.Now().UTC(), JobCount: localJobs}}
	if r.federation == nil {
		return out, nil
	}
	for _, worker := range r.federation.Get(ctx).Workers {
		out = append(out, server.WorkerSummary{
			ID: worker.ID, Label: worker.Label, Placement: worker.Placement,
			Health: worker.Health, LastSeen: worker.LastSeen,
			LastError: worker.LastError, JobCount: worker.JobCount,
		})
	}
	return out, nil
}

// RestoreDaemonJobs reattaches every non-terminal daemon job to its persisted
// session. The sweep records the crash boundary before any recovery attempt;
// failed reattachments remain interrupted and visible instead of disappearing
// or being reported as live.
func (r *SessionRegistry) RestoreDaemonJobs(ctx context.Context) (int, error) {
	if r.daemonJobs == nil {
		return 0, nil
	}
	if _, err := r.daemonJobs.SweepInterrupted(ctx, "daemon_restarted"); err != nil {
		return 0, fmt.Errorf("mark daemon jobs interrupted: %w", err)
	}
	jobs, err := r.daemonJobs.List(ctx, artifactjob.ListFilter{
		Status: []artifactjob.Status{artifactjob.StatusInterrupted},
		Limit:  1000,
	})
	if err != nil {
		return 0, fmt.Errorf("list interrupted daemon jobs: %w", err)
	}
	var restored int
	var restoreErrs []error
	for _, job := range jobs {
		if (job.Origin.Kind != "daemon" &&
			job.Origin.Kind != "campaign" &&
			job.Origin.Kind != "feedback" &&
			job.Origin.Kind != "application-artifact") ||
			job.Story == "" {
			continue
		}
		id, attachErr := r.AttachExternal(ctx, job.Story, "daemon:"+string(job.ID))
		if attachErr != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("restore job %s: %w", job.ID, attachErr))
			continue
		}
		if id != string(job.ID) {
			restoreErrs = append(restoreErrs, fmt.Errorf("restore job %s returned unstable route %s", job.ID, id))
			continue
		}
		if job.Origin.Kind == "feedback" {
			// Reattach the durable session, but keep the artifact job
			// interrupted until the operator explicitly retries the dispatch.
			restored++
			continue
		}
		r.syncDaemonJob(id)
		restored++
	}
	return restored, errors.Join(restoreErrs...)
}

func (r *SessionRegistry) syncDaemonJob(id string) {
	if r.daemonJobs == nil {
		return
	}
	r.mu.Lock()
	e := r.sessions[id]
	r.mu.Unlock()
	if e == nil || e.source == nil {
		return
	}
	snap, err := e.source.Snapshot()
	if err != nil {
		return
	}
	status := artifactjob.StatusRunning
	if snap.Session.Terminal {
		status = artifactjob.StatusDone
	}
	phase := snap.Session.CurrentState
	reason := ""
	_, _ = r.daemonJobs.Update(context.Background(), artifactjob.JobID(id), artifactjob.Update{
		Status:            &status,
		Phase:             &phase,
		InterruptedReason: &reason,
	})
}

// Reload mirrors the FULL TUI /reload path (tui.go handleReloadSlash), which is
// more than a bare Orchestrator.Reload:
//
//  1. read currentState from the session's live snapshot;
//  2. orch.Reload(StoryPath, currentState) → swaps the def + machine in;
//  3. RecordEffectiveStory so the trace stays self-contained across the reload;
//  4. when the prior state still exists, RerunOnEnter to re-fire the entered
//     state's on_enter chain (view-template / on_enter / prompt edits take
//     effect) — skipped when the state was removed by the edit.
//
// It introduces no new reload mechanism — it reuses the orchestrator methods the
// TUI already uses. prevStateExists is returned so the server can report
// {ok, prev_state_exists} and the UI can show the "current state removed;
// staying put" warning.
func (r *SessionRegistry) Reload(ctx context.Context, sessionID string) (bool, error) {
	r.mu.Lock()
	e, ok := r.sessions[sessionID]
	r.mu.Unlock()
	if !ok {
		return false, fmt.Errorf("reload: unknown session %q", sessionID)
	}

	// (1) Read the current state from the live snapshot — this is where the
	// session actually is, including after a prior reload.
	snap, err := e.source.Snapshot()
	if err != nil {
		return false, fmt.Errorf("reload: read session snapshot: %w", err)
	}
	currentState := app.StatePath(snap.Session.CurrentState)

	orch := e.rt.Orch

	// (2) Swap the freshly loaded def + machine into the orchestrator.
	res, err := orch.Reload(e.StoryPath, currentState)
	if err != nil {
		return false, fmt.Errorf("reload: %w", err)
	}

	// (3) Record the story change so the trace replay stays self-contained.
	if err := orch.RecordEffectiveStory(ctx, e.sid); err != nil {
		return res.PrevStateExists, fmt.Errorf("reload: record effective story: %w", err)
	}

	// Keep the entry's display def in sync with the reloaded definition, and
	// drop the cached meta controller so the next meta turn rebuilds against
	// the reloaded AppDef (a story edit may have changed meta_modes).
	freshContent := e.loadedContent
	if !e.synthetic {
		freshContent, _ = os.ReadFile(e.StoryPath)
	}
	r.mu.Lock()
	e.Def = res.Def
	e.loadedContent = freshContent
	e.metaController = nil
	r.mu.Unlock()

	// (4) Re-fire on_enter only when the current state survived the edit; a
	// removed state means there is nothing to re-enter (UI stays put).
	if res.PrevStateExists {
		if _, err := orch.RerunOnEnter(ctx, e.sid); err != nil {
			return true, fmt.Errorf("reload: rerun on_enter: %w", err)
		}
	}

	return res.PrevStateExists, nil
}

// ListStories returns the cached catalogue mapped onto server.StoryHeader, with
// active_sessions populated by scanning live entries whose StoryPath matches.
func (r *SessionRegistry) ListStories() []server.StoryHeader {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.storyHeadersLocked()
}

// ListAgents implements the optional [server.AgentLister] extension: the
// unified agent catalog (project TOML → embedded library → builtin registry)
// mapped onto the wire shape, so the SPA home screen renders the same catalog
// `kitsoki agent list` prints. Name collisions are deterministic: the row is
// the search-order winner and Shadows names the losing sources.
func (r *SessionRegistry) ListAgents() ([]server.AgentInfo, error) {
	infos, err := agentroot.List(agentroot.Sources{Materialize: materializeBuiltInAgentLibrary})
	if err != nil {
		return nil, err
	}
	out := make([]server.AgentInfo, 0, len(infos))
	for _, info := range infos {
		row := server.AgentInfo{
			Name:        info.Name,
			Source:      string(info.Source),
			Description: info.Description,
			Effect:      string(info.Effect),
			StoryPath:   agentroot.Scheme + info.Name,
		}
		for _, s := range info.Shadows {
			row.Shadows = append(row.Shadows, string(s))
		}
		out = append(out, row)
	}
	return out, nil
}

// Staleness compares the session's currently-loaded app.yaml bytes against the
// file on disk. stale is true when they differ; diff is a unified-diff string
// (context-3 lines) the UI can render in a modal. Returns no error on a missing
// file — that is treated as stale (content = "") rather than an error.
func (r *SessionRegistry) Staleness(_ context.Context, sessionID string) (stale bool, diff string, err error) {
	r.mu.Lock()
	e, ok := r.sessions[sessionID]
	if !ok {
		r.mu.Unlock()
		return false, "", fmt.Errorf("staleness: unknown session %q", sessionID)
	}
	loaded := e.loadedContent
	path := e.StoryPath
	synthetic := e.synthetic
	r.mu.Unlock()

	if synthetic {
		return false, "", nil
	}

	disk, readErr := os.ReadFile(path)
	if readErr != nil {
		disk = nil
	}

	if bytes.Equal(loaded, disk) {
		return false, "", nil
	}

	ud := difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(loaded)),
		B:        difflib.SplitLines(string(disk)),
		FromFile: "loaded",
		ToFile:   "on-disk",
		Context:  3,
	}
	text, _ := difflib.GetUnifiedDiffString(ud)
	// Build a short summary: count added/removed lines.
	added, removed := 0, 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}
	_ = added
	_ = removed
	return true, text, nil
}

// Rescan re-walks the configured story dirs (DiscoverStories), replaces the
// cached catalogue, and returns the refreshed headers. Live sessions are left
// untouched — a session keeps running against the story it was started with even
// if that story's manifest changed or disappeared from the catalogue.
func (r *SessionRegistry) Rescan() ([]server.StoryHeader, error) {
	metas, err := webconfig.DiscoverStories(r.dirs, buildImportResolver())
	if err != nil {
		if !defaultStoryDirsMissing(r.dirs, err) {
			return nil, err
		}
		metas = nil
	}
	if len(metas) == 0 && defaultStoryDirs(r.dirs) {
		implicit, impErr := r.synthesizeImplicitRoot()
		if impErr != nil {
			return nil, fmt.Errorf("synthesize implicit root: %w", impErr)
		}
		metas = append(metas, webconfig.StoryMeta{Path: implicit.path, Def: implicit.def})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stories = metas
	return r.storyHeadersLocked(), nil
}

func defaultStoryDirsMissing(dirs []string, err error) bool {
	if !defaultStoryDirs(dirs) || !errors.Is(err, os.ErrNotExist) {
		return false
	}
	return true
}

func defaultStoryDirs(dirs []string) bool {
	if len(dirs) != 1 {
		return false
	}
	clean := filepath.Clean(dirs[0])
	return clean == "stories"
}

// storyHeadersLocked maps the cached StoryMeta catalogue onto server.StoryHeader,
// filling ActiveSessions with the ids of live sessions started from each story.
// Caller holds r.mu.
func (r *SessionRegistry) storyHeadersLocked() []server.StoryHeader {
	// Index live session ids by the story they run.
	byStory := map[string][]string{}
	for id, e := range r.sessions {
		snap, err := e.source.Snapshot()
		if err != nil {
			continue
		}
		if snap.Session.Terminal {
			continue
		}
		byStory[e.StoryPath] = append(byStory[e.StoryPath], id)
	}
	out := make([]server.StoryHeader, 0, len(r.stories))
	for _, m := range r.stories {
		active := byStory[m.Path]
		if active == nil {
			active = []string{}
		}
		out = append(out, server.StoryHeader{
			Path:           m.Path,
			AppID:          m.Def.App.ID,
			Title:          storyTitle(m.Def),
			ActiveSessions: active,
		})
	}
	return out
}

// storyTitle picks the human-facing story title: the app's declared title when
// present, falling back to its id (which is always set).
func storyTitle(def *app.AppDef) string {
	if def == nil {
		return ""
	}
	if def.App.Title != "" {
		return def.App.Title
	}
	return def.App.ID
}

// EditorApp implements [server.EditorProvider]: it loads the story at
// storyPath fresh from disk and compiles it to an app.App for the read-only
// story-editor RPCs. Loading fresh (rather than reusing a cached catalogue
// Def) keeps the editor reflecting the on-disk story even between rescans, and
// keeps the editor independent of any live session. ok is false (no error) for
// an unknown story path or one that fails to load/validate — the server maps
// that to a structured not-found error.
//
// storyDir is the directory containing the manifest, used to resolve cassette
// globs for the agent workbench.
func (r *SessionRegistry) EditorApp(storyPath string) (app.App, string, bool) {
	abs, err := filepath.Abs(storyPath)
	if err != nil {
		return nil, "", false
	}
	// Only serve stories in the catalogue, so the editor surface cannot be used
	// to load arbitrary files off disk.
	r.mu.Lock()
	known := false
	for _, m := range r.stories {
		if m.Path == abs {
			known = true
			break
		}
	}
	r.mu.Unlock()
	if !known {
		return nil, "", false
	}

	loaded, err := r.loadStory(abs)
	if err != nil {
		return nil, "", false
	}
	return app.Compile(loaded.def), loaded.repoRoot, true
}

// ── Meta mode wiring ───────────────────────────────────────────────────────

// agentRegistryLocked returns the shared builtin agent registry every meta
// controller resolves agent names against, building it once. Caller holds r.mu.
//
// Builtins cover the modes the web surface exposes (story-author / -explainer,
// kitsoki-explainer); per-app agent overrides for meta modes are out of scope
// for the web surface.
func (r *SessionRegistry) agentRegistryLocked() agents.Registry {
	if r.agentReg == nil {
		r.agentReg = agents.NewBuiltins()
	}
	return r.agentReg
}

// agentForMeta picks the meta-mode agent: the deterministic no-LLM stub when
// the server runs in flow posture (--flow / --host-cassette), else the real
// claude-CLI adapter. This is the seam that keeps `kitsoki web --flow` (and the
// Playwright demo) free of any LLM call.
//
// When in stub posture, KITSOKI_META_STREAM_DELAY_MS sets the per-event pause
// the stub injects while emitting streaming events. Set it to 60-100 for demo
// recordings; leave unset (or 0) for fast tests.
func (r *SessionRegistry) agentForMeta() metamode.AgentCaller {
	// Deterministic posture ⇒ the no-LLM meta stub. This covers the nil-harness
	// flow posture (Flow != nil) AND the live-harness replay/recording posture
	// backed by a host cassette (--harness replay --recording --host-cassette):
	// in both, a story room's on_enter coding-agent task (e.g. dev-story's
	// landing_agent) must NOT spend a live LLM. Without this, replay tours that
	// pass through such a room dispatch a real agent backend mid-capture.
	deterministic := r.base.Flow != nil ||
		(r.base.HostCassette != "" &&
			(r.base.HarnessType == "replay" || r.base.HarnessType == "recording"))
	if deterministic {
		var opts []metamode.StubOption
		if v := os.Getenv("KITSOKI_META_STREAM_DELAY_MS"); v != "" {
			if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
				opts = append(opts, metamode.WithStubStreamDelay(time.Duration(ms)*time.Millisecond))
			}
		}
		return metamode.NewStubAgentCaller(opts...)
	}
	return metamode.NewAgentCallerAdapter()
}

// metaControllerForLocked returns e's meta controller, building it lazily and
// caching it on the entry. Caller holds r.mu.
func (r *SessionRegistry) metaControllerForLocked(e *entry) *metamode.Controller {
	if e.metaController == nil {
		e.metaController = &metamode.Controller{
			Chats:  metamode.NewChatStoreAdapter(e.rt.ChatStore),
			Agents: r.agentRegistryLocked(),
			AppDef: e.rt.Orch.AppDef(),
			Agent:  r.agentForMeta(),
		}
	}
	return e.metaController
}

// MetaSelf returns the session-less ("self") meta driver for the home screen —
// the cross-app kitsoki.* modes that need no running story. It is opened lazily
// on first use; ok is false when the resources can't be built (e.g. DB open
// failure), in which case home-screen meta reports not-available.
func (r *SessionRegistry) MetaSelf() (server.MetaDriver, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureSelfMetaLocked(); err != nil {
		return nil, false
	}
	return &metaDriver{ctrl: r.metaSelfCtrl, chats: r.metaSelfChat, entry: nil}, true
}

// ensureSelfMetaLocked lazily opens the self-meta store + chat store and builds
// the self controller over a synthetic AppDef carrying the builtin meta_modes.
// Caller holds r.mu. Idempotent: a no-op once built.
func (r *SessionRegistry) ensureSelfMetaLocked() error {
	if r.metaSelfCtrl != nil {
		return nil
	}
	s, err := openSessionStoreBackend(r.base.DBPath)
	if err != nil {
		return fmt.Errorf("meta self: open store: %w", err)
	}
	cs, err := newChatStore(s)
	if err != nil {
		_ = s.Close()
		return fmt.Errorf("meta self: open chat store: %w", err)
	}
	// Synthetic AppDef: the self modes (kitsoki.*) key under metamode.SelfAppID
	// at resolve time, so the App.ID here is only a fallback label. Injecting
	// the builtins gives the controller the kitsoki.* mode declarations.
	def := &app.AppDef{}
	def.App.ID = metamode.SelfAppID
	app.InjectBuiltinMetaModes(def)

	r.metaSelfStr = s
	r.metaSelfChat = cs
	r.metaSelfCtrl = &metamode.Controller{
		Chats:  metamode.NewChatStoreAdapter(cs),
		Agents: r.agentRegistryLocked(),
		AppDef: def,
		Agent:  r.agentForMeta(),
	}
	return nil
}

// Compile-time assertions that SessionRegistry satisfies the provider seams.
var (
	_ server.SessionProvider               = (*SessionRegistry)(nil)
	_ server.MetaSelfProvider              = (*SessionRegistry)(nil)
	_ server.EditorProvider                = (*SessionRegistry)(nil)
	_ server.SeededSessionProvider         = (*SessionRegistry)(nil)
	_ server.RegisteredApplicationProvider = (*SessionRegistry)(nil)
	_ server.ExternalAttachProvider        = (*SessionRegistry)(nil)
	_ server.CurrentSessionProvider        = (*SessionRegistry)(nil)
)

// seedFlowInitialState honors a flow fixture's initial_state / initial_world on
// a freshly created web session, mirroring what `kitsoki test flows` and
// `kitsoki record` do (internal/testrunner.seedInitialState,
// cmd/kitsoki/record.go). It is the runtime fix for `kitsoki web --flow`
// previously ignoring the fixture seed: a conversation-driven dev-story tour
// can now START at the fixture's state with its world pre-seeded.
//
// It writes synthetic turn-0 seed events — a TransitionApplied to teleport the
// journey onto initial_state, and one EffectApplied per initial_world key — and
// arms any Timeout declared on the seeded state. The orchestrator's loadJourney
// replays the event log, so persisting these before any turn (and before
// on_enter / the first observed frame) bootstraps the session exactly as the
// seed path does.
//
// fixture == nil (no --flow / --host-cassette) or an empty seed is a no-op:
// the session keeps starting at orch.InitialState(), so default web behavior is
// unchanged. Returns the effective initial state (the seeded state when one was
// applied, else orch.InitialState()) so the caller stamps the LiveSession and
// first frame with it.
func seedFlowInitialState(orch *orchestrator.Orchestrator, st store.Store, sid app.SessionID, fixture *testrunner.FlowFixture) (app.StatePath, error) {
	if fixture == nil || (fixture.InitialState == "" && len(fixture.InitialWorld) == 0) {
		return orch.InitialState(), nil
	}

	var events []store.Event
	if fixture.InitialState != "" {
		events = append(events, store.Event{
			Kind: store.TransitionApplied,
			Turn: 0,
			Payload: mustSeedJSON(map[string]any{
				"from":   "",
				"to":     fixture.InitialState,
				"intent": "__seed__",
			}),
		})
	}
	for k, v := range fixture.InitialWorld {
		events = append(events, store.Event{
			Kind:    store.EffectApplied,
			Turn:    0,
			Payload: mustSeedJSON(map[string]any{"set": map[string]any{k: v}}),
		})
	}

	if fixture.InitialState != "" {
		for i := range events {
			if events[i].StatePath == "" {
				events[i].StatePath = app.StatePath(fixture.InitialState)
			}
		}
	}

	sink := store.NewStoreSinkAdapter(st, sid)
	if err := sink.AppendBatch(events); err != nil {
		return "", err
	}

	// Arm any Timeout on the seeded state, matching seedInitialState — seed
	// events bypass the normal transition path that would otherwise arm it.
	if fixture.InitialState != "" {
		orch.ArmTimeoutForInitialState(sid, app.StatePath(fixture.InitialState))
		return app.StatePath(fixture.InitialState), nil
	}
	return orch.InitialState(), nil
}

func mustSeedJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("seedFlowInitialState: marshal: %v", err))
	}
	return b
}
