/**
 * Agent-message markdown rendering for the main operator chat.
 *
 * The engine ships ALREADY-RENDERED text (an 80-col terminal room view, or a
 * streamed oracle reply) — the browser never evaluates pongo. We only apply a
 * light, HTML-safe markdown pass on top:
 *
 *   - fenced code blocks (```lang\n…\n```) → a styled <pre><code> box. This is
 *     what stops a raw ```json reply from leaking to the operator as literal
 *     backtick-fenced text.
 *   - ATX heading lines (## Title) → a bold heading span (markers stripped).
 *   - inline `code` and **bold**.
 *
 * Everything OUTSIDE a fence is kept verbatim, line by line: newlines,
 * indentation and column alignment survive (the caller pairs this with
 * white-space: pre-wrap), so engine-laid-out lists / key:value tables are never
 * re-flowed into run-on prose. Only the fenced segments are reflowed into the
 * code box. An UNCLOSED fence (mid-stream, before the closing ``` arrives) is
 * left as plain escaped text and snaps into a code box once it closes.
 *
 * The source is HTML-escaped FIRST (it can embed user-supplied idea text), so
 * the result is safe to inject with v-html.
 */

function escapeHtml(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

// renderInline applies bold + inline-code to an ALREADY HTML-escaped string.
function renderInline(escaped: string): string {
  return escaped
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
}

export function renderAgentMarkdown(src: string): string {
  const text = src ?? "";
  // Split into fenced-block segments (odd indices) and plain segments (even).
  const parts = text.split(/(```[^\n]*\n[\s\S]*?```)/g);
  return parts
    .map((part, idx) => {
      if (idx % 2 === 1) {
        const match = part.match(/^```([^\n]*)\n([\s\S]*?)```$/);
        const body = match ? match[2] : part.slice(3, -3);
        const lang = match?.[1]?.trim() ?? "";
        const langAttr = lang ? ` class="language-${escapeHtml(lang)}"` : "";
        return `<pre class="cv-pre"><code${langAttr}>${escapeHtml(body)}</code></pre>`;
      }
      // Plain text: keep verbatim line structure, format headings + inline.
      return escapeHtml(part)
        .split("\n")
        .map((line) => {
          const h = line.match(/^(#{1,6})\s+(.*)$/);
          if (h) return `<span class="cv-h">${renderInline(h[2])}</span>`;
          return renderInline(line);
        })
        .join("\n");
    })
    .join("");
}
