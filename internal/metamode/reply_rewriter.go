package metamode

import (
	"regexp"
	"strings"
)

// replyRewriter mutates an LLM reply by replacing each structured
// authoring-call token with a short human-readable result summary, in
// the order the tokens appear. The metamode controller uses it during
// dispatchAuthoringCalls so the transcript ends up with sentences
// like "[proposal abcd1234 applied: …]" instead of the raw tokens.
//
// We re-scan the reply for each token type rather than carrying byte
// offsets through the regex matches because the replacement text
// shifts the offsets of every later match. Simpler to do one
// strings.Replace(..., 1) per call.
type replyRewriter struct{ s string }

func newReplyRewriter(reply string) *replyRewriter {
	return &replyRewriter{s: reply}
}

func (r *replyRewriter) String() string { return r.s }

// proposeBlockSingleRE captures the FIRST <<<propose>>>…<<<endpropose>>>
// block (greedy, but bounded by the closing token). Used to replace
// each block in document order — call once per parsed propose.
var proposeBlockSingleRE = regexp.MustCompile(`(?s)<<<propose>>>.*?<<<endpropose>>>`)

// replaceFirstProposeBlock swaps the next propose-block in the reply
// for the given summary. No-op if the reply contains no remaining
// propose blocks.
func (r *replyRewriter) replaceFirstProposeBlock(summary string) {
	loc := proposeBlockSingleRE.FindStringIndex(r.s)
	if loc == nil {
		return
	}
	r.s = r.s[:loc[0]] + summary + r.s[loc[1]:]
}

// replaceApplyToken swaps the first occurrence of <<<apply <id>>>> in
// the reply for the given summary. We match on the exact token text
// so two interleaved apply calls with different IDs each get the
// right summary.
func (r *replyRewriter) replaceApplyToken(id, summary string) {
	r.s = replaceFirstWith(r.s, "<<<apply "+id+">>>", summary)
}

// replaceDiscardToken is the discard analogue of replaceApplyToken.
func (r *replyRewriter) replaceDiscardToken(id, summary string) {
	r.s = replaceFirstWith(r.s, "<<<discard "+id+">>>", summary)
}

// replaceFirstWith replaces the first occurrence of needle in haystack
// with replacement. Returns haystack unchanged if needle is absent.
func replaceFirstWith(haystack, needle, replacement string) string {
	idx := strings.Index(haystack, needle)
	if idx < 0 {
		return haystack
	}
	return haystack[:idx] + replacement + haystack[idx+len(needle):]
}
