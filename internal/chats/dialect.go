package chats

import (
	"strconv"
	"strings"
)

// dialect selects the SQL flavor a Store speaks. The zero value is SQLite so
// the existing NewStore constructor keeps its exact behavior without any
// call-site changes; NewPostgresStore opts in to dialectPostgres explicitly.
type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

// rebind rewrites `?` placeholders into the dialect's native form. SQLite
// keeps `?` untouched (identity, zero cost on the default path); Postgres
// numbers them `$1..$N` left to right. Question marks inside single-quoted
// SQL literals are preserved so queries like `status != 'archived'` stay
// intact even if a literal ever contains a question mark.
func (d dialect) rebind(query string) string {
	if d != dialectPostgres {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 16)
	n := 0
	inLiteral := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'':
			inLiteral = !inLiteral
			b.WriteByte(c)
		case c == '?' && !inLiteral:
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// chatsPGTables qualifies this subsystem's table references into the "chats"
// schema (schema-per-subsystem on a shared database). Rewrites are anchored
// on the preceding SQL keyword so unrelated identifiers are never touched;
// longer table names come first because strings.Replacer tries patterns in
// argument order and "chats" is a prefix of "chats_schema_meta".
var chatsPGTables = strings.NewReplacer(
	"INTO chats_schema_meta", "INTO chats.chats_schema_meta",
	"INTO chat_pty_sessions", "INTO chats.chat_pty_sessions",
	"INTO chat_input_queue", "INTO chats.chat_input_queue",
	"INTO chat_messages", "INTO chats.chat_messages",
	"INTO chat_locks", "INTO chats.chat_locks",
	"INTO chats", "INTO chats.chats",
	"FROM chats_schema_meta", "FROM chats.chats_schema_meta",
	"FROM chat_pty_sessions", "FROM chats.chat_pty_sessions",
	"FROM chat_input_queue", "FROM chats.chat_input_queue",
	"FROM chat_messages", "FROM chats.chat_messages",
	"FROM chat_locks", "FROM chats.chat_locks",
	"FROM chats", "FROM chats.chats",
	"UPDATE chats_schema_meta", "UPDATE chats.chats_schema_meta",
	"UPDATE chat_pty_sessions", "UPDATE chats.chat_pty_sessions",
	"UPDATE chat_input_queue", "UPDATE chats.chat_input_queue",
	"UPDATE chat_messages", "UPDATE chats.chat_messages",
	"UPDATE chat_locks", "UPDATE chats.chat_locks",
	"UPDATE chats", "UPDATE chats.chats",
)

// q is shorthand for rebinding a query for the Store's dialect. Every SQL
// string in this package passes through q at its call site; on the default
// SQLite path it returns the input unchanged. For Postgres the table names
// are additionally schema-qualified into the dedicated "chats" schema.
func (s *Store) q(query string) string {
	if s.dialect != dialectPostgres {
		return query
	}
	return s.dialect.rebind(chatsPGTables.Replace(query))
}
