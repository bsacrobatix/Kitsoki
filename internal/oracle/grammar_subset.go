// grammar_subset.go re-exports the grammar-subset gate so the oracle package
// (and its tests) can call GrammarSubsetOK in-package while the implementation
// lives in the dependency-free internal/oracle/grammar leaf package.
//
// Why the split: the app loader must run the SAME gate at load time to reject
// grammar:true effects pointed at out-of-subset schemas, but app cannot import
// oracle (oracle already imports app, which would be a cycle). Hoisting the pure
// logic into a leaf package both can import keeps a single source of truth; this
// wrapper preserves oracle.GrammarSubsetOK for local_llm.go and the existing
// tests. See internal/oracle/grammar/grammar.go for the contract.

package oracle

import (
	"encoding/json"

	"kitsoki/internal/oracle/grammar"
)

// GrammarSubsetOK reports whether schema is inside llama.cpp's reliably
// translatable grammar subset (see internal/oracle/grammar.SubsetOK). It returns
// nil for an in-subset schema, or a descriptive error naming the first offending
// construct and its JSON path otherwise. ValidateSubmission remains the real
// guarantee regardless; this gate only governs whether grammar is worth
// requesting.
func GrammarSubsetOK(schema json.RawMessage) error {
	return grammar.SubsetOK(schema)
}
