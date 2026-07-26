// Package applicationcapture owns durable, authenticated browser capture requests.
package applicationcapture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"kitsoki/internal/clock"
	"kitsoki/internal/runstatus/harscrub"
)

const (
	Schema             = "kitsoki/application-capture/v1"
	maxActions         = 64
	maxActionIDBytes   = 256
	maxScenarioRef     = 1024
	maxEventBytes      = 8 << 20
	maxEvents          = 50000
	maxCaptureDuration = 2 * time.Minute
	requestTimeout     = 45 * time.Second
	surfaceLease       = 5 * time.Second
)

var (
	ErrNoSurface   = errors.New("compatible application surface is not attached")
	ErrInterrupted = errors.New("application capture was interrupted")
)

type Surface struct {
	AppID           string   `json:"app_id"`
	PublicSessionID string   `json:"session_id"`
	EngineSessionID string   `json:"engine_session_id"`
	Actor           string   `json:"actor"`
	Revision        uint64   `json:"revision"`
	ActionIDs       []string `json:"action_ids"`
	AttachedAt      string   `json:"attached_at"`
}

type Plan struct {
	ScenarioRef string   `json:"scenario_ref"`
	ActionIDs   []string `json:"action_ids"`
}

type Request struct {
	Schema            string          `json:"schema"`
	ID                string          `json:"request_id"`
	AppID             string          `json:"app_id"`
	SessionID         string          `json:"session_id"`
	EngineSessionID   string          `json:"engine_session_id,omitempty"`
	Actor             string          `json:"actor,omitempty"`
	Revision          uint64          `json:"revision"`
	ScenarioRef       string          `json:"scenario_ref"`
	ActionIDs         []string        `json:"action_ids"`
	PlanDigest        string          `json:"-"`
	Status            string          `json:"status"`
	ArtifactRef       string          `json:"artifact_ref,omitempty"`
	ArtifactPath      string          `json:"artifact_path,omitempty"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
	InterruptedReason string          `json:"interrupted_reason,omitempty"`
	Receipts          []ActionReceipt `json:"receipts,omitempty"`
}

type ActionReceipt struct {
	Index          int    `json:"index"`
	ActionID       string `json:"action_id"`
	ReceiptID      string `json:"receipt_id"`
	HandlerID      string `json:"handler_id"`
	SemanticRef    string `json:"semantic_ref"`
	IdempotencyKey string `json:"idempotency_key"`
	FrameRevision  uint64 `json:"frame_revision"`
}

type Ack struct {
	RequestID string          `json:"request_id"`
	AppID     string          `json:"app_id"`
	SessionID string          `json:"session_id"`
	Revision  uint64          `json:"revision"`
	Receipts  []ActionReceipt `json:"receipts"`
	Recording json.RawMessage `json:"recording"`
}

type FailureAck struct {
	RequestID string          `json:"request_id"`
	AppID     string          `json:"app_id"`
	SessionID string          `json:"session_id"`
	Revision  uint64          `json:"revision"`
	Reason    string          `json:"reason"`
	Receipts  []ActionReceipt `json:"receipts,omitempty"`
}

type Result struct {
	ArtifactRef  string
	ArtifactPath string
}

type Broker interface {
	Attach(context.Context, Surface) error
	Request(context.Context, string, string, string, Plan, string) (Result, error)
	Pending(context.Context, Surface) ([]Request, error)
	Lookup(context.Context, Surface, string) (Request, error)
	Acknowledge(context.Context, Surface, Ack) (Result, error)
	Fail(context.Context, Surface, FailureAck) error
}

type fileState struct {
	Schema   string             `json:"schema"`
	Requests map[string]Request `json:"requests"`
}

type FileBroker struct {
	Root     string
	Clock    clock.Clock
	mu       sync.Mutex
	wake     map[string]chan struct{}
	surfaces map[string]Surface
}

func NewFileBroker(root string, clk clock.Clock) *FileBroker {
	return &FileBroker{
		Root: root, Clock: clk,
		wake: map[string]chan struct{}{}, surfaces: map[string]Surface{},
	}
}

func (b *FileBroker) Attach(_ context.Context, surface Surface) error {
	if err := validateSurface(surface); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneExpiredSurfaces()
	key := surfaceKey(surface.AppID, surface.EngineSessionID, surface.Actor)
	if prior, ok := b.surfaces[key]; ok {
		if prior.PublicSessionID != surface.PublicSessionID {
			return fmt.Errorf("application surface cannot change public session within an attached scope")
		}
		if surface.Revision < prior.Revision {
			return fmt.Errorf("application surface revision cannot move backward")
		}
	}
	surface.AttachedAt = b.now().UTC().Format(time.RFC3339Nano)
	b.surfaces[key] = surface
	return nil
}

func (b *FileBroker) Request(
	ctx context.Context,
	appID, originEngineSessionID, actor string,
	plan Plan,
	idempotencyKey string,
) (Result, error) {
	if err := validatePlan(plan); err != nil {
		return Result{}, err
	}
	appID, originEngineSessionID, actor = strings.TrimSpace(appID), strings.TrimSpace(originEngineSessionID), strings.TrimSpace(actor)
	if appID == "" || originEngineSessionID == "" || actor == "" {
		return Result{}, fmt.Errorf("application capture requires app, session, and actor authority")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return Result{}, fmt.Errorf("application capture idempotency_key is required")
	}
	planDigest := digest("plan", plan.ScenarioRef, strings.Join(plan.ActionIDs, "\x00"))

	b.mu.Lock()
	state, err := b.load()
	if err != nil {
		b.mu.Unlock()
		return Result{}, err
	}
	b.pruneExpiredSurfaces()
	var matches []Surface
	for _, candidate := range b.surfaces {
		if candidate.AppID == appID && candidate.Actor == actor &&
			candidate.EngineSessionID != originEngineSessionID &&
			b.surfaceActive(candidate) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		b.mu.Unlock()
		return Result{}, fmt.Errorf("%w: found %d non-origin surfaces for application %q", ErrNoSurface, len(matches), appID)
	}
	surface := matches[0]
	engineSessionID := surface.EngineSessionID
	requestID := digest("request", appID, engineSessionID, actor, idempotencyKey)
	req, exists := state.Requests[requestID]
	if exists {
		priorDigest := req.PlanDigest
		if priorDigest == "" {
			priorDigest = digest("plan", req.ScenarioRef, strings.Join(req.ActionIDs, "\x00"))
		}
		if priorDigest != planDigest {
			b.mu.Unlock()
			return Result{}, fmt.Errorf("application capture idempotency conflict: key was already used for a different plan")
		}
	}
	if exists && req.Status == "completed" {
		b.mu.Unlock()
		return Result{ArtifactRef: req.ArtifactRef, ArtifactPath: req.ArtifactPath}, nil
	}
	now := b.now().UTC().Format(time.RFC3339Nano)
	if !exists || req.Status == "interrupted" {
		allowed := make(map[string]bool, len(surface.ActionIDs))
		for _, id := range surface.ActionIDs {
			allowed[id] = true
		}
		if !allowed[plan.ActionIDs[0]] {
			b.mu.Unlock()
			return Result{}, fmt.Errorf("%w: first action %q is not currently offered", ErrNoSurface, plan.ActionIDs[0])
		}
		req = Request{
			Schema: Schema, ID: requestID, AppID: appID,
			SessionID: surface.PublicSessionID, EngineSessionID: engineSessionID,
			Actor: actor, Revision: surface.Revision, ScenarioRef: plan.ScenarioRef,
			ActionIDs: append([]string(nil), plan.ActionIDs...), PlanDigest: planDigest, Status: "pending",
			CreatedAt: now, UpdatedAt: now,
		}
		state.Requests[requestID] = req
		if err := b.save(state); err != nil {
			b.mu.Unlock()
			return Result{}, err
		}
	}
	ch := b.waiter(requestID)
	b.mu.Unlock()

	timer := b.Clock.NewTimer(requestTimeout)
	defer timer.Stop()
	poller := b.Clock.NewTicker(100 * time.Millisecond)
	defer poller.Stop()
	for {
		select {
		case <-ctx.Done():
			b.interrupt(requestID, "request context ended")
			return Result{}, fmt.Errorf("%w: %v", ErrInterrupted, ctx.Err())
		case <-timer.C():
			b.interrupt(requestID, "capture deadline exceeded")
			return Result{}, fmt.Errorf("%w: deadline exceeded", ErrInterrupted)
		case <-ch:
		case <-poller.C():
		}
		req, err := b.lookupRequest(requestID)
		if err != nil {
			return Result{}, err
		}
		switch req.Status {
		case "completed":
			return Result{ArtifactRef: req.ArtifactRef, ArtifactPath: req.ArtifactPath}, nil
		case "interrupted":
			return Result{}, fmt.Errorf("%w: %s", ErrInterrupted, req.InterruptedReason)
		}
		b.mu.Lock()
		ch = b.waiter(requestID)
		b.mu.Unlock()
	}
}

func (b *FileBroker) Pending(_ context.Context, surface Surface) ([]Request, error) {
	if err := validateSurface(surface); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state, err := b.load()
	if err != nil {
		return nil, err
	}
	attached, ok := b.surfaces[surfaceKey(surface.AppID, surface.EngineSessionID, surface.Actor)]
	if !ok || !b.surfaceActive(attached) ||
		attached.PublicSessionID != surface.PublicSessionID || attached.Revision != surface.Revision {
		return nil, ErrNoSurface
	}
	var out []Request
	for _, req := range state.Requests {
		if req.Status == "pending" && req.AppID == surface.AppID &&
			req.SessionID == surface.PublicSessionID && req.EngineSessionID == surface.EngineSessionID &&
			req.Actor == surface.Actor && req.Revision <= surface.Revision {
			req.EngineSessionID, req.Actor, req.ArtifactPath = "", "", ""
			out = append(out, req)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

func (b *FileBroker) Lookup(_ context.Context, surface Surface, requestID string) (Request, error) {
	if err := validateSurface(surface); err != nil {
		return Request{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state, err := b.load()
	if err != nil {
		return Request{}, err
	}
	attached, ok := b.surfaces[surfaceKey(surface.AppID, surface.EngineSessionID, surface.Actor)]
	if !ok || !b.surfaceActive(attached) ||
		attached.PublicSessionID != surface.PublicSessionID || attached.Revision != surface.Revision {
		return Request{}, ErrNoSurface
	}
	req, ok := state.Requests[requestID]
	if !ok {
		return Request{}, fmt.Errorf("capture request %q not found", requestID)
	}
	if req.AppID != surface.AppID || req.SessionID != surface.PublicSessionID ||
		req.EngineSessionID != surface.EngineSessionID || req.Actor != surface.Actor ||
		req.Revision > surface.Revision {
		return Request{}, fmt.Errorf("capture request is outside the authorized app/session/revision scope")
	}
	req.EngineSessionID, req.Actor, req.ArtifactPath = "", "", ""
	return req, nil
}

func (b *FileBroker) Acknowledge(_ context.Context, surface Surface, ack Ack) (Result, error) {
	if err := validateSurface(surface); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(ack.RequestID) == "" {
		return Result{}, fmt.Errorf("request_id is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state, err := b.load()
	if err != nil {
		return Result{}, err
	}
	req, ok := state.Requests[ack.RequestID]
	if !ok {
		return Result{}, fmt.Errorf("capture request %q not found", ack.RequestID)
	}
	if req.Status == "completed" {
		return Result{ArtifactRef: req.ArtifactRef, ArtifactPath: req.ArtifactPath}, nil
	}
	if req.Status != "pending" {
		return Result{}, fmt.Errorf("%w: %s", ErrInterrupted, req.InterruptedReason)
	}
	attached, attachedOK := b.surfaces[surfaceKey(surface.AppID, surface.EngineSessionID, surface.Actor)]
	if !attachedOK || !b.surfaceActive(attached) ||
		attached.PublicSessionID != surface.PublicSessionID ||
		attached.Revision != surface.Revision || attached.Revision < req.Revision ||
		req.AppID != surface.AppID ||
		req.SessionID != surface.PublicSessionID ||
		req.EngineSessionID != surface.EngineSessionID || req.Actor != surface.Actor ||
		ack.AppID != surface.AppID ||
		ack.SessionID != surface.PublicSessionID {
		return Result{}, fmt.Errorf("capture acknowledgement is outside the authorized app/session/revision scope")
	}
	if ack.Revision != req.Revision {
		return Result{}, fmt.Errorf("capture acknowledgement revision %d does not match pinned request revision %d", ack.Revision, req.Revision)
	}
	if err := validateReceipts(req, ack.Receipts); err != nil {
		return Result{}, err
	}
	recording, err := scrubAndValidateRecording(ack.Recording)
	if err != nil {
		return Result{}, err
	}
	artifactDigest := digest("artifact", req.ID, string(recording))
	dir := filepath.Join(b.Root, "artifacts", artifactDigest)
	path := filepath.Join(dir, "session.rrweb.json")
	if err := writeAtomic(path, append(recording, '\n'), 0o600); err != nil {
		return Result{}, err
	}
	receipts, err := json.MarshalIndent(ack.Receipts, "", "  ")
	if err != nil {
		return Result{}, err
	}
	if err := writeAtomic(filepath.Join(dir, "action-receipts.json"), append(receipts, '\n'), 0o600); err != nil {
		return Result{}, err
	}
	req.Status = "completed"
	req.Receipts = append([]ActionReceipt(nil), ack.Receipts...)
	req.ArtifactRef = "kitsoki://application-capture/" + digest("scope", req.AppID, req.EngineSessionID) + "/sha256/" + artifactDigest
	req.ArtifactPath = path
	req.UpdatedAt = b.now().UTC().Format(time.RFC3339Nano)
	state.Requests[req.ID] = req
	if err := b.save(state); err != nil {
		return Result{}, err
	}
	b.signal(req.ID)
	return Result{ArtifactRef: req.ArtifactRef, ArtifactPath: req.ArtifactPath}, nil
}

func (b *FileBroker) Fail(_ context.Context, surface Surface, ack FailureAck) error {
	if err := validateSurface(surface); err != nil {
		return err
	}
	if strings.TrimSpace(ack.RequestID) == "" || strings.TrimSpace(ack.Reason) == "" || len(ack.Reason) > 512 {
		return fmt.Errorf("capture failure requires bounded request_id and reason")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state, err := b.load()
	if err != nil {
		return err
	}
	req, ok := state.Requests[ack.RequestID]
	if !ok {
		return fmt.Errorf("capture request %q not found", ack.RequestID)
	}
	if req.Status != "pending" {
		return fmt.Errorf("capture request is already %s", req.Status)
	}
	attached, attachedOK := b.surfaces[surfaceKey(surface.AppID, surface.EngineSessionID, surface.Actor)]
	if !attachedOK || !b.surfaceActive(attached) ||
		attached.PublicSessionID != surface.PublicSessionID ||
		attached.Revision != surface.Revision ||
		req.AppID != surface.AppID || req.SessionID != surface.PublicSessionID ||
		req.EngineSessionID != surface.EngineSessionID || req.Actor != surface.Actor ||
		req.Revision > surface.Revision || ack.AppID != surface.AppID ||
		ack.SessionID != surface.PublicSessionID || ack.Revision != req.Revision {
		return fmt.Errorf("capture failure acknowledgement is outside the authorized app/session/revision scope")
	}
	if err := validatePartialReceipts(req, ack.Receipts); err != nil {
		return err
	}
	req.Status = "interrupted"
	req.Receipts = append([]ActionReceipt(nil), ack.Receipts...)
	req.InterruptedReason = harscrub.ScrubString(ack.Reason, harscrub.ScrubOptions{
		Home: os.Getenv("HOME"), SecretPatterns: harscrub.DefaultSecretPatterns(),
	})
	req.UpdatedAt = b.now().UTC().Format(time.RFC3339Nano)
	state.Requests[req.ID] = req
	if err := b.save(state); err != nil {
		return err
	}
	b.signal(req.ID)
	return nil
}

func validateSurface(surface Surface) error {
	if strings.TrimSpace(surface.AppID) == "" || strings.TrimSpace(surface.PublicSessionID) == "" ||
		strings.TrimSpace(surface.EngineSessionID) == "" || strings.TrimSpace(surface.Actor) == "" ||
		surface.Revision == 0 || len(surface.ActionIDs) > maxActions {
		return fmt.Errorf("application surface requires app, public session, engine session, actor, revision, and bounded actions")
	}
	for _, id := range surface.ActionIDs {
		if strings.TrimSpace(id) == "" || len(id) > maxActionIDBytes {
			return fmt.Errorf("application surface action is empty or exceeds %d bytes", maxActionIDBytes)
		}
	}
	return nil
}

func validatePlan(plan Plan) error {
	if strings.TrimSpace(plan.ScenarioRef) == "" || len(plan.ScenarioRef) > maxScenarioRef {
		return fmt.Errorf("capture scenario_ref is required and bounded")
	}
	if len(plan.ActionIDs) == 0 || len(plan.ActionIDs) > maxActions {
		return fmt.Errorf("capture action count %d is outside 1..%d", len(plan.ActionIDs), maxActions)
	}
	for i, id := range plan.ActionIDs {
		if strings.TrimSpace(id) == "" || len(id) > maxActionIDBytes {
			return fmt.Errorf("capture action %d is empty or exceeds %d bytes", i, maxActionIDBytes)
		}
	}
	return nil
}

func validateReceipts(req Request, receipts []ActionReceipt) error {
	if len(receipts) != len(req.ActionIDs) {
		return fmt.Errorf("capture acknowledgement has %d receipts, want %d", len(receipts), len(req.ActionIDs))
	}
	return validatePartialReceipts(req, receipts)
}

func validatePartialReceipts(req Request, receipts []ActionReceipt) error {
	if len(receipts) > len(req.ActionIDs) {
		return fmt.Errorf("capture acknowledgement has too many receipts")
	}
	for i, receipt := range receipts {
		if receipt.Index != i || receipt.ActionID != req.ActionIDs[i] ||
			strings.TrimSpace(receipt.ReceiptID) == "" ||
			strings.TrimSpace(receipt.HandlerID) == "" ||
			strings.TrimSpace(receipt.SemanticRef) == "" ||
			strings.TrimSpace(receipt.IdempotencyKey) == "" ||
			receipt.FrameRevision == 0 {
			return fmt.Errorf("capture receipt %d is not canonical proof for action %q", i, req.ActionIDs[i])
		}
		if i == 0 && receipt.FrameRevision != req.Revision {
			return fmt.Errorf("capture receipt 0 revision %d does not match pinned request revision %d", receipt.FrameRevision, req.Revision)
		}
		if i > 0 && receipt.FrameRevision < receipts[i-1].FrameRevision {
			return fmt.Errorf("capture receipt %d revision moved backward", i)
		}
	}
	return nil
}

func scrubAndValidateRecording(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 || len(raw) > maxEventBytes || !json.Valid(raw) {
		return nil, fmt.Errorf("rrweb recording is empty, invalid, or exceeds %d bytes", maxEventBytes)
	}
	var envelope struct {
		StartTime  int64             `json:"startTime"`
		EndTime    int64             `json:"endTime"`
		DurationMS int64             `json:"durationMs"`
		Events     []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode rrweb recording: %w", err)
	}
	if len(envelope.Events) == 0 || len(envelope.Events) > maxEvents {
		return nil, fmt.Errorf("rrweb event count %d is outside 1..%d", len(envelope.Events), maxEvents)
	}
	if envelope.DurationMS < 0 || time.Duration(envelope.DurationMS)*time.Millisecond > maxCaptureDuration ||
		envelope.EndTime < envelope.StartTime {
		return nil, fmt.Errorf("rrweb recording duration is invalid or exceeds %s", maxCaptureDuration)
	}
	scrubbed := harscrub.ScrubString(string(raw), harscrub.ScrubOptions{
		Home: os.Getenv("HOME"), SecretPatterns: harscrub.DefaultSecretPatterns(),
	})
	if !json.Valid([]byte(scrubbed)) {
		return nil, fmt.Errorf("privacy scrubber produced invalid rrweb JSON")
	}
	return []byte(scrubbed), nil
}

func (b *FileBroker) interrupt(id, reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, err := b.load()
	if err != nil {
		return
	}
	req, ok := state.Requests[id]
	if !ok || req.Status != "pending" {
		return
	}
	req.Status, req.InterruptedReason = "interrupted", reason
	req.UpdatedAt = b.now().UTC().Format(time.RFC3339Nano)
	state.Requests[id] = req
	if b.save(state) == nil {
		b.signal(id)
	}
}

func (b *FileBroker) lookupRequest(id string) (Request, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, err := b.load()
	if err != nil {
		return Request{}, err
	}
	req, ok := state.Requests[id]
	if !ok {
		return Request{}, fmt.Errorf("capture request %q disappeared", id)
	}
	return req, nil
}

func (b *FileBroker) waiter(id string) chan struct{} {
	if b.wake == nil {
		b.wake = map[string]chan struct{}{}
	}
	ch := b.wake[id]
	if ch == nil {
		ch = make(chan struct{})
		b.wake[id] = ch
	}
	return ch
}

func (b *FileBroker) signal(id string) {
	if ch := b.wake[id]; ch != nil {
		close(ch)
		delete(b.wake, id)
	}
}

func (b *FileBroker) now() time.Time {
	if b.Clock != nil {
		return b.Clock.Now()
	}
	return time.Now()
}

func (b *FileBroker) surfaceActive(surface Surface) bool {
	attachedAt, err := time.Parse(time.RFC3339Nano, surface.AttachedAt)
	return err == nil && b.now().Sub(attachedAt) <= surfaceLease
}

func (b *FileBroker) pruneExpiredSurfaces() {
	for key, surface := range b.surfaces {
		if !b.surfaceActive(surface) {
			delete(b.surfaces, key)
		}
	}
}

func (b *FileBroker) load() (fileState, error) {
	state := fileState{Schema: Schema, Requests: map[string]Request{}}
	raw, err := os.ReadFile(filepath.Join(b.Root, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("decode application capture state: %w", err)
	}
	if state.Schema != Schema {
		return state, fmt.Errorf("unsupported application capture state schema %q", state.Schema)
	}
	if state.Requests == nil {
		state.Requests = map[string]Request{}
	}
	return state, nil
}

func (b *FileBroker) save(state fileState) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(b.Root, "state.json"), append(raw, '\n'), 0o600)
}

func writeAtomic(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".capture-*.tmp")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func surfaceKey(appID, engineSessionID, actor string) string {
	return digest("surface", appID, engineSessionID, actor)
}

func digest(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
