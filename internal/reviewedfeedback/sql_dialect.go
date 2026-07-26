package reviewedfeedback

import (
	"strconv"
	"strings"
)

type sqlDialect int

const (
	sqlDialectSQLite sqlDialect = iota
	sqlDialectPostgres
)

var postgresFeedbackTables = strings.NewReplacer(
	"reviewed_feedback_dispatches", "reviewedfeedback.reviewed_feedback_dispatches",
	"feedback_reconciliations", "reviewedfeedback.feedback_reconciliations",
)

func feedbackSQL(dialect sqlDialect, query string) string {
	if dialect != sqlDialectPostgres {
		return query
	}
	return rebindFeedbackSQL(postgresFeedbackTables.Replace(query))
}

func rebindFeedbackSQL(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var out strings.Builder
	out.Grow(len(query) + 16)
	ordinal := 0
	for i := 0; i < len(query); i++ {
		if query[i] != '?' {
			out.WriteByte(query[i])
			continue
		}
		ordinal++
		out.WriteByte('$')
		out.WriteString(strconv.Itoa(ordinal))
	}
	return out.String()
}
