package journal

import (
	"strconv"
	"strings"
)

// dialect selects the SQL flavor the database-backed Writer/Reader speak. The
// zero value is SQLite so the existing NewSQLiteWriter / NewSQLiteReader
// constructors keep their exact behavior without any call-site changes;
// NewPostgresWriter / NewPostgresReader opt in to dialectPostgres explicitly.
// Both dialects target the same journal table shape (internal/store's
// schema.sql and schema_pg.sql), so only placeholder syntax differs.
type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

// rebind rewrites `?` placeholders into the dialect's native form. SQLite
// keeps `?` untouched (identity, zero cost on the default path); Postgres
// numbers them `$1..$N` left to right. Question marks inside single-quoted
// SQL literals are preserved so queries carrying literals (e.g. the kind
// filters in replayTypedSQL) stay intact even if a literal ever contains a
// question mark. Mirrors internal/chats' dialect.rebind.
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
