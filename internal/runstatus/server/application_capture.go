package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	appplatform "kitsoki/internal/application"
	"kitsoki/internal/applicationcapture"
)

type applicationCaptureSubscriptions struct {
	mu   sync.Mutex
	subs map[string]*applicationCaptureSubscription
}

type applicationCaptureSubscription struct {
	id        string
	sessionID string
	actor     string
	expiresAt time.Time
	mu        sync.Mutex
	sent      map[string]bool
}

func newApplicationCaptureSubscriptions() *applicationCaptureSubscriptions {
	return &applicationCaptureSubscriptions{subs: map[string]*applicationCaptureSubscription{}}
}

const (
	maxApplicationCaptureSubscriptions = 256
	applicationCaptureSubscriptionTTL  = 10 * time.Minute
)

func (r *applicationCaptureSubscriptions) subscribe(sessionID, actor string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for id, sub := range r.subs {
		if !sub.expiresAt.After(now) {
			delete(r.subs, id)
		}
	}
	if len(r.subs) >= maxApplicationCaptureSubscriptions {
		return "", fmt.Errorf("application capture subscription capacity reached")
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("create application capture subscription: %w", err)
	}
	id := hex.EncodeToString(token[:])
	r.subs[id] = &applicationCaptureSubscription{
		id: id, sessionID: sessionID, actor: actor,
		expiresAt: now.Add(applicationCaptureSubscriptionTTL), sent: map[string]bool{},
	}
	return id, nil
}

func (r *applicationCaptureSubscriptions) lookup(id string) *applicationCaptureSubscription {
	r.mu.Lock()
	defer r.mu.Unlock()
	sub := r.subs[id]
	if sub == nil || !sub.expiresAt.After(time.Now()) {
		return nil
	}
	return sub
}

func (r *applicationCaptureSubscriptions) active(sub *applicationCaptureSubscription) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.subs[sub.id]
	if current != sub || !sub.expiresAt.After(time.Now()) {
		if current == sub {
			delete(r.subs, sub.id)
		}
		return false
	}
	return true
}

func (r *applicationCaptureSubscriptions) unsubscribe(id, actor string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	sub := r.subs[id]
	if sub == nil || sub.actor != actor {
		return false
	}
	delete(r.subs, id)
	return true
}

func (s *Server) applicationCaptureSurface(
	ctx context.Context,
	params map[string]any,
) (applicationcapture.Surface, *rpcError) {
	if s.applicationCaptures == nil {
		return applicationcapture.Surface{}, &rpcError{
			Code: codeReadOnly, Message: "application capture broker is unavailable",
		}
	}
	entry, rerr := s.resolve(params)
	if rerr != nil {
		return applicationcapture.Surface{}, rerr
	}
	sessionID, rerr := sessionIDParam(params)
	if rerr != nil {
		return applicationcapture.Surface{}, rerr
	}
	actor, ok := s.resolveActor(ctx, params)
	if !ok {
		return applicationcapture.Surface{}, invalidParams(fmt.Errorf("authenticated actor is required"))
	}
	service, err := NewSessionApplicationService(entry, "", s.applicationRuntime())
	if err != nil {
		return applicationcapture.Surface{}, serverErr(err)
	}
	frame, err := service.Frames.CurrentFrame(ctx, sessionID)
	if err != nil {
		return applicationcapture.Surface{}, serverErr(err)
	}
	snapshot, err := entry.Source.Snapshot()
	if err != nil {
		return applicationcapture.Surface{}, serverErr(err)
	}
	surface := applicationcapture.Surface{
		AppID: frame.ApplicationID, PublicSessionID: sessionID,
		EngineSessionID: snapshot.Session.SessionID, Actor: actor,
		Revision: frame.Revision, ActionIDs: applicationFrameActionIDs(frame),
	}
	if err := s.applicationCaptures.Attach(ctx, surface); err != nil {
		return applicationcapture.Surface{}, invalidParams(err)
	}
	return surface, nil
}

func applicationFrameActionIDs(frame appplatform.Frame) []string {
	seen := map[string]bool{}
	var out []string
	add := func(actions []appplatform.Action) {
		for _, action := range actions {
			if captureCompatibleAction(action) && !seen[action.ID] {
				seen[action.ID] = true
				out = append(out, action.ID)
			}
		}
	}
	var walk func([]appplatform.Element)
	walk = func(elements []appplatform.Element) {
		for _, element := range elements {
			if !captureNodeVisible(element.State) {
				continue
			}
			add(element.Actions)
			walk(element.Items)
		}
	}
	for _, region := range frame.Regions {
		if !captureNodeVisible(region.State) {
			continue
		}
		for _, card := range region.Cards {
			if !captureNodeVisible(card.State) {
				continue
			}
			add(card.Actions)
			walk(card.Body)
		}
	}
	return out
}

func captureCompatibleAction(action appplatform.Action) bool {
	if !action.Enabled || (action.State.Enabled != nil && !*action.State.Enabled) {
		return false
	}
	if len(action.InputSchema) == 0 {
		return true
	}
	var schema struct {
		Required []string `json:"required"`
	}
	return json.Unmarshal(action.InputSchema, &schema) == nil && len(schema.Required) == 0
}

func captureNodeVisible(state appplatform.NodeState) bool {
	return state.Visible == nil || *state.Visible
}

func (s *Server) handleApplicationCaptures(w http.ResponseWriter, r *http.Request) {
	sub := s.captureSubs.lookup(r.URL.Query().Get("subscription_id"))
	if sub == nil {
		http.Error(w, "unknown subscription", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		if !s.captureSubs.active(sub) {
			return
		}
		s.streamApplicationCaptures(r.Context(), w, flusher, sub)
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) streamApplicationCaptures(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	sub *applicationCaptureSubscription,
) {
	surface, rerr := s.applicationCaptureSurface(ctx, map[string]any{
		"session_id": sub.sessionID, "actor": sub.actor,
	})
	if rerr != nil {
		return
	}
	pending, err := s.applicationCaptures.Pending(ctx, surface)
	if err != nil {
		return
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	for _, request := range pending {
		if sub.sent[request.ID] {
			continue
		}
		raw, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": "runstatus.application.capture.request",
			"params": map[string]any{
				"schema":       request.Schema,
				"request_id":   request.ID,
				"app_id":       request.AppID,
				"session_id":   request.SessionID,
				"revision":     request.Revision,
				"scenario_ref": request.ScenarioRef,
				"action_ids":   request.ActionIDs,
			},
		})
		if err != nil {
			continue
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return
		}
		sub.sent[request.ID] = true
	}
	flusher.Flush()
}

type applicationCaptureReceiptLedger struct {
	mu       sync.Mutex
	receipts map[string]map[string]applicationCaptureReceiptEntry
	order    []applicationCaptureReceiptKey
}

type applicationCaptureReceiptKey struct {
	sessionID string
	receiptID string
}

type applicationCaptureReceiptEntry struct {
	actionID string
	receipt  appplatform.Receipt
}

const maxApplicationCaptureReceipts = 2048

func newApplicationCaptureReceiptLedger() *applicationCaptureReceiptLedger {
	return &applicationCaptureReceiptLedger{
		receipts: map[string]map[string]applicationCaptureReceiptEntry{},
	}
}

func (l *applicationCaptureReceiptLedger) record(
	sessionID, actionID string,
	receipt appplatform.Receipt,
) {
	if sessionID == "" || actionID == "" || receipt.ID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.receipts[sessionID] == nil {
		l.receipts[sessionID] = map[string]applicationCaptureReceiptEntry{}
	}
	if _, exists := l.receipts[sessionID][receipt.ID]; !exists {
		l.order = append(l.order, applicationCaptureReceiptKey{sessionID: sessionID, receiptID: receipt.ID})
	}
	l.receipts[sessionID][receipt.ID] = applicationCaptureReceiptEntry{actionID: actionID, receipt: receipt}
	for len(l.order) > maxApplicationCaptureReceipts {
		oldest := l.order[0]
		l.order = l.order[1:]
		delete(l.receipts[oldest.sessionID], oldest.receiptID)
		if len(l.receipts[oldest.sessionID]) == 0 {
			delete(l.receipts, oldest.sessionID)
		}
	}
}

func (l *applicationCaptureReceiptLedger) verify(
	surface applicationcapture.Surface,
	request applicationcapture.Request,
	receipts []applicationcapture.ActionReceipt,
	complete bool,
) error {
	if complete && len(receipts) != len(request.ActionIDs) {
		return fmt.Errorf("capture acknowledgement requires one canonical receipt per action")
	}
	if len(receipts) > len(request.ActionIDs) {
		return fmt.Errorf("capture acknowledgement has too many canonical receipts")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for index, claimed := range receipts {
		entry, ok := l.receipts[surface.PublicSessionID][claimed.ReceiptID]
		if !ok {
			return fmt.Errorf("capture receipt %q is absent from the server action ledger", claimed.ReceiptID)
		}
		receipt := entry.receipt
		if claimed.Index != index || claimed.ActionID != request.ActionIDs[index] ||
			entry.actionID != claimed.ActionID || claimed.HandlerID != receipt.HandlerID ||
			claimed.SemanticRef != receipt.SemanticRef ||
			claimed.IdempotencyKey != receipt.IdempotencyKey ||
			claimed.FrameRevision != receipt.FrameRevision ||
			receipt.SessionID != surface.PublicSessionID || receipt.Actor != surface.Actor ||
			receipt.Transport != appplatform.TransportWeb {
			return fmt.Errorf(
				"capture receipt %q does not match canonical server action result: action=%q/%q handler=%q/%q semantic=%q/%q idempotency=%q/%q revision=%d/%d session=%q/%q actor=%q/%q transport=%q",
				claimed.ReceiptID, claimed.ActionID, entry.actionID,
				claimed.HandlerID, receipt.HandlerID, claimed.SemanticRef, receipt.SemanticRef,
				claimed.IdempotencyKey, receipt.IdempotencyKey,
				claimed.FrameRevision, receipt.FrameRevision,
				surface.PublicSessionID, receipt.SessionID, surface.Actor, receipt.Actor, receipt.Transport,
			)
		}
	}
	return nil
}
