package applicationconversation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"kitsoki/internal/clock"
)

type sqlDialect int

const (
	dialectSQLite sqlDialect = iota
	dialectPostgres
)

type SQLStore struct {
	db      *sql.DB
	dialect sqlDialect
	clock   clock.Clock
}

func NewSQLiteStore(db *sql.DB, clk clock.Clock) (*SQLStore, error) {
	return newSQLStore(db, dialectSQLite, clk)
}

func NewPostgresStore(db *sql.DB, clk clock.Clock) (*SQLStore, error) {
	return newSQLStore(db, dialectPostgres, clk)
}

func newSQLStore(db *sql.DB, dialect sqlDialect, clk clock.Clock) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("application conversation store: database is required")
	}
	if clk == nil {
		clk = clock.Real()
	}
	s := &SQLStore{db: db, dialect: dialect, clock: clk}
	for _, statement := range s.schema() {
		if _, err := db.Exec(statement); err != nil {
			return nil, fmt.Errorf("application conversation store: apply schema: %w", err)
		}
	}
	return s, nil
}

func (s *SQLStore) schema() []string {
	table := `
CREATE TABLE IF NOT EXISTS application_conversation_turns (
    turn_ref TEXT NOT NULL PRIMARY KEY,
    application_id TEXT NOT NULL,
    chat_ref TEXT NOT NULL,
    binding_digest TEXT NOT NULL,
    predecessor_ref TEXT NOT NULL DEFAULT '',
    question_digest TEXT NOT NULL,
    graph_digest TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending','answer_ready','completed','interrupted')),
    user_seq INTEGER,
    assistant_seq INTEGER,
    answer TEXT NOT NULL DEFAULT '',
    receipt_json TEXT NOT NULL DEFAULT '',
    interrupted_reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT`
	index := `CREATE INDEX IF NOT EXISTS application_conversation_turns_chat
ON application_conversation_turns(application_id, chat_ref, updated_at DESC, turn_ref DESC)`
	active := `CREATE UNIQUE INDEX IF NOT EXISTS application_conversation_turns_active
ON application_conversation_turns(application_id, chat_ref)
WHERE status IN ('pending', 'answer_ready')`
	if s.dialect == dialectPostgres {
		table = `
CREATE SCHEMA IF NOT EXISTS applicationconversation`
		turns := `
CREATE TABLE IF NOT EXISTS applicationconversation.turns (
    turn_ref TEXT NOT NULL PRIMARY KEY,
    application_id TEXT NOT NULL,
    chat_ref TEXT NOT NULL,
    binding_digest TEXT NOT NULL,
    predecessor_ref TEXT NOT NULL DEFAULT '',
    question_digest TEXT NOT NULL,
    graph_digest TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending','answer_ready','completed','interrupted')),
    user_seq BIGINT,
    assistant_seq BIGINT,
    answer TEXT NOT NULL DEFAULT '',
    receipt_json TEXT NOT NULL DEFAULT '',
    interrupted_reason TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
)`
		index = `CREATE INDEX IF NOT EXISTS application_conversation_turns_chat
ON applicationconversation.turns(application_id, chat_ref, updated_at DESC, turn_ref DESC)`
		active = `CREATE UNIQUE INDEX IF NOT EXISTS application_conversation_turns_active
ON applicationconversation.turns(application_id, chat_ref)
WHERE status IN ('pending', 'answer_ready')`
		return []string{table, turns, index, active}
	}
	return []string{table, index, active}
}

func (s *SQLStore) table() string {
	if s.dialect == dialectPostgres {
		return "applicationconversation.turns"
	}
	return "application_conversation_turns"
}

func (s *SQLStore) q(query string) string {
	if s.dialect == dialectSQLite {
		return query
	}
	var out strings.Builder
	arg := 1
	for _, r := range query {
		if r == '?' {
			fmt.Fprintf(&out, "$%d", arg)
			arg++
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func (s *SQLStore) Latest(ctx context.Context, applicationID, chatRef string) (Turn, error) {
	query := fmt.Sprintf(`SELECT turn_ref, application_id, chat_ref, binding_digest,
predecessor_ref, question_digest, graph_digest, status, user_seq, assistant_seq, answer,
receipt_json, interrupted_reason, created_at, updated_at
FROM %s AS current_turn WHERE application_id = ? AND chat_ref = ?
AND NOT EXISTS (
	SELECT 1 FROM %s AS child
	WHERE child.application_id = current_turn.application_id
	AND child.chat_ref = current_turn.chat_ref
	AND child.predecessor_ref = current_turn.turn_ref
)
ORDER BY current_turn.updated_at DESC, current_turn.turn_ref DESC LIMIT 1`, s.table(), s.table())
	return s.scan(s.db.QueryRowContext(ctx, s.q(query), applicationID, chatRef))
}

func (s *SQLStore) Get(ctx context.Context, ref string) (Turn, error) {
	query := fmt.Sprintf(`SELECT turn_ref, application_id, chat_ref, binding_digest,
predecessor_ref, question_digest, graph_digest, status, user_seq, assistant_seq, answer,
receipt_json, interrupted_reason, created_at, updated_at
FROM %s WHERE turn_ref = ?`, s.table())
	return s.scan(s.db.QueryRowContext(ctx, s.q(query), ref))
}

func (s *SQLStore) Begin(ctx context.Context, turn Turn) (Turn, error) {
	if turn.Ref == "" || turn.ApplicationID == "" || turn.ChatRef == "" ||
		turn.BindingDigest == "" || turn.QuestionDigest == "" {
		return Turn{}, fmt.Errorf("application conversation store: incomplete turn identity")
	}
	now := s.clock.Now().UTC().UnixMicro()
	query := fmt.Sprintf(`INSERT INTO %s
(turn_ref, application_id, chat_ref, binding_digest, predecessor_ref,
 question_digest, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, s.table())
	_, err := s.db.ExecContext(
		ctx,
		s.q(query),
		turn.Ref,
		turn.ApplicationID,
		turn.ChatRef,
		turn.BindingDigest,
		turn.PredecessorRef,
		turn.QuestionDigest,
		now,
		now,
	)
	if err == nil {
		return s.Get(ctx, turn.Ref)
	}
	existing, getErr := s.Get(ctx, turn.Ref)
	if getErr != nil {
		return Turn{}, err
	}
	if existing.ApplicationID != turn.ApplicationID ||
		existing.ChatRef != turn.ChatRef ||
		existing.BindingDigest != turn.BindingDigest ||
		existing.PredecessorRef != turn.PredecessorRef ||
		existing.QuestionDigest != turn.QuestionDigest {
		return Turn{}, fmt.Errorf("application conversation turn identity conflict")
	}
	if existing.Status != TurnInterrupted {
		return existing, nil
	}
	update := fmt.Sprintf(`UPDATE %s SET status = 'pending',
interrupted_reason = '', updated_at = ? WHERE turn_ref = ? AND status = 'interrupted'`, s.table())
	if _, err := s.db.ExecContext(ctx, s.q(update), now, turn.Ref); err != nil {
		return Turn{}, err
	}
	return s.Get(ctx, turn.Ref)
}

func (s *SQLStore) SetUserSeq(ctx context.Context, ref string, seq int) error {
	return s.setSeq(ctx, ref, "user_seq", seq, TurnPending)
}

func (s *SQLStore) SetAssistantSeq(ctx context.Context, ref string, seq int) error {
	return s.setSeq(ctx, ref, "assistant_seq", seq, TurnAnswerReady)
}

func (s *SQLStore) setSeq(
	ctx context.Context,
	ref, column string,
	seq int,
	status TurnStatus,
) error {
	if seq < 0 || (column != "user_seq" && column != "assistant_seq") {
		return fmt.Errorf("application conversation store: invalid message reference")
	}
	query := fmt.Sprintf(`UPDATE %s SET %s = ?, updated_at = ?
WHERE turn_ref = ? AND status = ? AND (%s IS NULL OR %s = ?)`,
		s.table(), column, column, column,
	)
	res, err := s.db.ExecContext(
		ctx,
		s.q(query),
		seq,
		s.clock.Now().UTC().UnixMicro(),
		ref,
		string(status),
		seq,
	)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected != 1 {
		return fmt.Errorf("application conversation store: message reference transition rejected")
	}
	return nil
}

func (s *SQLStore) StoreAnswer(ctx context.Context, ref, answer, graphDigest string) error {
	if graphDigest == "" {
		return fmt.Errorf("application conversation store: graph digest is required")
	}
	query := fmt.Sprintf(`UPDATE %s SET status = 'answer_ready', answer = ?,
graph_digest = ?, interrupted_reason = '', updated_at = ? WHERE turn_ref = ?
AND status = 'pending' AND user_seq IS NOT NULL`, s.table())
	res, err := s.db.ExecContext(
		ctx,
		s.q(query),
		answer,
		graphDigest,
		s.clock.Now().UTC().UnixMicro(),
		ref,
	)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected != 1 {
		return fmt.Errorf("application conversation store: answer transition rejected")
	}
	return nil
}

func (s *SQLStore) Complete(ctx context.Context, ref string, receipt []byte) error {
	query := fmt.Sprintf(`UPDATE %s SET status = 'completed', receipt_json = ?,
interrupted_reason = '', updated_at = ? WHERE turn_ref = ? AND
status = 'answer_ready' AND user_seq IS NOT NULL AND assistant_seq IS NOT NULL
AND answer != ''`, s.table())
	res, err := s.db.ExecContext(
		ctx,
		s.q(query),
		string(receipt),
		s.clock.Now().UTC().UnixMicro(),
		ref,
	)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 1 {
		return nil
	}
	existing, getErr := s.Get(ctx, ref)
	if getErr == nil && existing.Status == TurnCompleted &&
		string(existing.ReceiptJSON) == string(receipt) {
		return nil
	}
	return fmt.Errorf("application conversation store: completion transition rejected")
}

func (s *SQLStore) Interrupt(ctx context.Context, ref, reason string) error {
	query := fmt.Sprintf(`UPDATE %s SET status = 'interrupted',
interrupted_reason = ?, updated_at = ? WHERE turn_ref = ? AND status = 'pending'`, s.table())
	_, err := s.db.ExecContext(
		ctx,
		s.q(query),
		reason,
		s.clock.Now().UTC().UnixMicro(),
		ref,
	)
	return err
}

func (s *SQLStore) InterruptPending(ctx context.Context, reason string) (int64, error) {
	query := fmt.Sprintf(`UPDATE %s SET status = 'interrupted',
interrupted_reason = ?, updated_at = ? WHERE status = 'pending'`, s.table())
	res, err := s.db.ExecContext(
		ctx,
		s.q(query),
		reason,
		s.clock.Now().UTC().UnixMicro(),
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type rowScanner interface {
	Scan(...any) error
}

func (s *SQLStore) scan(row rowScanner) (Turn, error) {
	var (
		turn                  Turn
		status                string
		userSeq, assistantSeq sql.NullInt64
		receipt               string
		createdAt, updatedAt  int64
	)
	err := row.Scan(
		&turn.Ref,
		&turn.ApplicationID,
		&turn.ChatRef,
		&turn.BindingDigest,
		&turn.PredecessorRef,
		&turn.QuestionDigest,
		&turn.GraphDigest,
		&status,
		&userSeq,
		&assistantSeq,
		&turn.Answer,
		&receipt,
		&turn.InterruptedReason,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Turn{}, ErrTurnNotFound
	}
	if err != nil {
		return Turn{}, err
	}
	turn.Status = TurnStatus(status)
	if userSeq.Valid {
		value := int(userSeq.Int64)
		turn.UserSeq = &value
	}
	if assistantSeq.Valid {
		value := int(assistantSeq.Int64)
		turn.AssistantSeq = &value
	}
	turn.ReceiptJSON = []byte(receipt)
	turn.CreatedAt = time.UnixMicro(createdAt).UTC()
	turn.UpdatedAt = time.UnixMicro(updatedAt).UTC()
	return turn, nil
}
