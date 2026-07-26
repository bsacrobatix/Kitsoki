package journal

import (
	"context"
	"database/sql"
	"fmt"

	"kitsoki/internal/app"
)

// postgres.go adds the Postgres entry points for the shared SQL-backed
// Writer/Reader in sqlite.go. Both dialects run the same implementation and
// the same SQL against the same journal table shape (internal/store's
// schema.sql / schema_pg.sql — the DDL is owned by the store layer, which
// creates the table when it opens the database); only placeholder syntax
// differs, handled by dialect.rebind. Semantics are identical to SQLite:
// doc-targeting entries get DocVersion = MAX(doc_version)+1 per (session,
// doc), out-of-turn entries (Turn=0, Seq=0) get Seq auto-assigned from
// MAX(seq)+1, and the checkpoint+patches resume contract is unchanged.
//
// Concurrency: as on SQLite, unserialised concurrent Appends to the same
// (session, doc) can race on the MAX reads. Callers that need cross-process
// serialisation on Postgres go through the store layer's append path, which
// row-locks the session (SELECT ... FOR UPDATE) before calling
// [AppendJournalPgTx].

// NewPostgresWriter returns a Writer backed by db (a pgx-stdlib handle).
// db must already be open and have the journal table created (the store
// layer's OpenPostgres applies schema_pg.sql). Flush is a no-op on this
// dialect: Postgres commits are durable at transaction commit.
func NewPostgresWriter(db *sql.DB) (Writer, error) {
	if db == nil {
		return nil, fmt.Errorf("journal.NewPostgresWriter: db must not be nil")
	}
	return &sqlWriter{db: db, d: dialectPostgres}, nil
}

// NewPostgresReader returns a Reader backed by db (a pgx-stdlib handle).
func NewPostgresReader(db *sql.DB) (Reader, error) {
	if db == nil {
		return nil, fmt.Errorf("journal.NewPostgresReader: db must not be nil")
	}
	return &sqlReader{db: db, d: dialectPostgres}, nil
}

// AppendJournalPgTx is the Postgres counterpart of [AppendJournalTx]: it
// inserts a batch of entries into the journal table within the provided
// transaction, using $N placeholders. The store layer calls it from within
// its AppendEventsAndJournal transaction so event and journal writes share
// atomicity, exactly as the SQLite store does with AppendJournalTx.
func AppendJournalPgTx(ctx context.Context, tx *sql.Tx, sid app.SessionID, entries []Entry) error {
	return appendJournalTx(ctx, tx, dialectPostgres, sid, entries)
}
