package applicationconversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/clock"
	"kitsoki/internal/dbruntime/pgtest"
	"kitsoki/internal/store"
)

func TestSQLStoreSQLiteAndPostgresParity(t *testing.T) {
	tests := []struct {
		name string
		open func(*testing.T) (*sql.DB, func())
		new  func(*sql.DB) (*SQLStore, error)
	}{
		{
			name: "sqlite",
			open: func(t *testing.T) (*sql.DB, func()) {
				st, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
				require.NoError(t, err)
				return st.DB(), func() { require.NoError(t, st.Close()) }
			},
			new: func(db *sql.DB) (*SQLStore, error) {
				return NewSQLiteStore(db, clock.Real())
			},
		},
		{
			name: "postgres",
			open: func(t *testing.T) (*sql.DB, func()) {
				return pgtest.Open(t), func() {}
			},
			new: func(db *sql.DB) (*SQLStore, error) {
				return NewPostgresStore(db, clock.Real())
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, closeDB := test.open(t)
			defer closeDB()
			turns, err := test.new(db)
			require.NoError(t, err)
			ctx := context.Background()
			turn := Turn{
				Ref: "turn-a", ApplicationID: "app", ChatRef: "chat",
				BindingDigest: "binding", QuestionDigest: "question",
			}
			begun, err := turns.Begin(ctx, turn)
			require.NoError(t, err)
			require.Equal(t, TurnPending, begun.Status)
			require.NoError(t, turns.SetUserSeq(ctx, turn.Ref, 1))
			require.NoError(t, turns.StoreAnswer(ctx, turn.Ref, "answer", "graph"))
			require.NoError(t, turns.SetAssistantSeq(ctx, turn.Ref, 2))
			receipt, err := json.Marshal(Receipt{Schema: ReceiptSchema, TurnRef: turn.Ref})
			require.NoError(t, err)
			require.NoError(t, turns.Complete(ctx, turn.Ref, receipt))
			require.NoError(t, turns.Complete(ctx, turn.Ref, receipt))
			completed, err := turns.Get(ctx, turn.Ref)
			require.NoError(t, err)
			require.Equal(t, TurnCompleted, completed.Status)
			require.Equal(t, "graph", completed.GraphDigest)
			require.Equal(t, "answer", completed.Answer)

			pending := Turn{
				Ref: "turn-b", ApplicationID: "app", ChatRef: "chat",
				BindingDigest: "binding", PredecessorRef: "turn-a",
				QuestionDigest: "question-b",
			}
			_, err = turns.Begin(ctx, pending)
			require.NoError(t, err)
			affected, err := turns.InterruptPending(ctx, "daemon_restarted")
			require.NoError(t, err)
			require.EqualValues(t, 1, affected)
			interrupted, err := turns.Get(ctx, pending.Ref)
			require.NoError(t, err)
			require.Equal(t, TurnInterrupted, interrupted.Status)
			require.Equal(t, "daemon_restarted", interrupted.InterruptedReason)
			resumed, err := turns.Begin(ctx, pending)
			require.NoError(t, err)
			require.Equal(t, TurnPending, resumed.Status)
		})
	}
}
