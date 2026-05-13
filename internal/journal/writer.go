package journal

import (
	"encoding/json"
	"sync"

	"kitsoki/internal/app"
)

// Writer is the append-only interface for emitting journal entries.
type Writer interface {
	// Append writes a single entry to the journal.
	Append(e Entry) error

	// AppendCheckpoint writes a full-document checkpoint for doc.
	// full is the complete serialised document value (the "body.full" field).
	// The implementation computes and assigns the Kind and DocVersion.
	AppendCheckpoint(sid app.SessionID, turn app.TurnNumber, seq int, doc DocID, full json.RawMessage) error

	// Flush ensures all buffered writes are durable. For in-memory
	// implementations this is a no-op; SQLite-backed implementations may
	// use it to flush WAL pages.
	Flush() error
}

// ---- In-memory implementation -----------------------------------------------

// memStore is the shared backing store for memWriter and memReader.
type memStore struct {
	mu      sync.RWMutex
	entries []Entry
	// docVersions tracks the next version per (session, doc).
	docVersions map[sessionDocKey]Version
}

type sessionDocKey struct {
	sid app.SessionID
	doc DocID
}

func newMemStore() *memStore {
	return &memStore{
		docVersions: make(map[sessionDocKey]Version),
	}
}

// nextVersion atomically increments and returns the version for (sid, doc).
// Must be called with ms.mu held for writing.
func (ms *memStore) nextVersion(sid app.SessionID, doc DocID) Version {
	k := sessionDocKey{sid, doc}
	ms.docVersions[k]++
	return ms.docVersions[k]
}

// checkpointKindFor returns the checkpoint Kind constant for doc.
func checkpointKindFor(doc DocID) string {
	switch {
	case doc == "world":
		return KindWorldCheckpoint
	case doc == "state":
		return KindStateCheckpoint
	case len(doc) > 6 && doc[:6] == "chats/":
		return KindChatsCheckpoint
	case len(doc) > 5 && doc[:5] == "jobs/":
		return KindJobsCheckpoint
	default:
		// Fallback for unknown doc prefixes; callers should use well-known docs.
		return string(doc) + ".checkpoint"
	}
}

// memWriter implements Writer backed by a *memStore.
type memWriter struct {
	store *memStore
}

// NewMemWriter returns a Writer and paired Reader that share an in-memory
// store. This pair is intended for tests.
func NewMemWriter(store *memStore) Writer {
	return &memWriter{store: store}
}

func (w *memWriter) Append(e Entry) error {
	w.store.mu.Lock()
	defer w.store.mu.Unlock()

	// Assign a DocVersion for patch entries that target a doc.
	if e.Doc != "" && IsPatchKind(e.Kind) {
		e.DocVersion = w.store.nextVersion(e.Session, e.Doc)
	}

	w.store.entries = append(w.store.entries, e)
	return nil
}

func (w *memWriter) AppendCheckpoint(sid app.SessionID, turn app.TurnNumber, seq int, doc DocID, full json.RawMessage) error {
	w.store.mu.Lock()
	defer w.store.mu.Unlock()

	ver := w.store.nextVersion(sid, doc)
	body, err := json.Marshal(struct {
		Full json.RawMessage `json:"full"`
	}{Full: full})
	if err != nil {
		return err
	}

	e := Entry{
		Session:    sid,
		Turn:       turn,
		Seq:        seq,
		Kind:       checkpointKindFor(doc),
		Doc:        doc,
		DocVersion: ver,
		Body:       body,
	}
	w.store.entries = append(w.store.entries, e)
	return nil
}

func (w *memWriter) Flush() error { return nil }
