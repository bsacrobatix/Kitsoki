// Package gitserve serves the git smart HTTP protocol for a root directory of
// bare repositories by wrapping the git CLI (`git http-backend`) through
// net/http/cgi. It matches the repository-wide convention of shelling out to
// git rather than reimplementing the wire protocol.
//
// Security model: repository paths are strictly validated (safe path segments,
// no traversal, repo must exist under the root and be a bare repository, and
// the symlink-resolved directory must still be inside the root), and
// only the smart-HTTP endpoints (/info/refs, /git-upload-pack,
// /git-receive-pack) are exposed. Authentication is an injected seam
// (AuthFunc); the default is allow-all because the listener bind address is
// the operator's access control — callers that expose the handler beyond
// localhost must inject a real AuthFunc.
//
// Ref-update notification: instead of installing post-receive hook files into
// served repositories (which would leave repository-local executable state
// behind and break the "plain bare repo" property), the handler snapshots
// `git for-each-ref` before and after each receive-pack POST and reports the
// diff to an injected RefHook. The tradeoff: notifications are per-HTTP-push
// and best-effort — two pushes racing on the same repo may each observe a
// superset of their own updates — but repositories stay unmodified and
// portable. Operators that need transactionally exact per-push attribution
// should use real post-receive hooks instead.
package gitserve

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// RefUpdate describes a single reference change observed after a push.
// Old is the all-zero object ID for created refs; New is all-zero for
// deleted refs.
type RefUpdate struct {
	Ref string
	Old string
	New string
}

// RefHook receives ref-change notifications after a successful receive-pack
// POST. repo is the repository path relative to the serving root (for example
// "team/project.git"). The hook is invoked synchronously from the request
// goroutine and only when at least one ref changed.
type RefHook interface {
	OnRefsChanged(repo string, updates []RefUpdate)
}

// RefHookFunc adapts a function to the RefHook interface.
type RefHookFunc func(repo string, updates []RefUpdate)

func (f RefHookFunc) OnRefsChanged(repo string, updates []RefUpdate) { f(repo, updates) }

// AuthFunc authenticates a request and returns the acting identity. It is
// exported to git http-backend as REMOTE_USER when non-empty. Returning
// ok=false rejects the request with 401 Unauthorized. A nil AuthFunc allows
// every request with an empty actor: the bind address is then the operator's
// only access control, which is appropriate for loopback-only servers.
type AuthFunc func(r *http.Request) (actor string, ok bool)

// Option configures a Handler.
type Option func(*Handler)

// WithReadOnly rejects all receive-pack (push) traffic with a git-protocol
// error while continuing to serve fetches and clones.
func WithReadOnly() Option { return func(h *Handler) { h.readOnly = true } }

// WithAuth injects the authentication seam. See AuthFunc.
func WithAuth(auth AuthFunc) Option { return func(h *Handler) { h.auth = auth } }

// WithRefHook injects the ref-update notification seam. See RefHook.
func WithRefHook(hook RefHook) Option { return func(h *Handler) { h.hook = hook } }

// WithGitPath overrides the git executable used for CGI serving and ref
// snapshots (default: `git` resolved from PATH).
func WithGitPath(gitPath string) Option { return func(h *Handler) { h.gitPath = gitPath } }

// Handler serves the git smart HTTP protocol for bare repositories under a
// root directory. Construct with New; the zero value is not usable.
type Handler struct {
	root     string
	gitPath  string
	readOnly bool
	auth     AuthFunc
	hook     RefHook
}

// New builds a Handler serving repositories under root. root must exist and
// be a directory; the git executable must be resolvable.
func New(root string, opts ...Option) (*Handler, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("gitserve: resolve root: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("gitserve: stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("gitserve: root %q is not a directory", abs)
	}
	h := &Handler{root: abs}
	for _, opt := range opts {
		opt(h)
	}
	if h.gitPath == "" {
		gitPath, lookErr := exec.LookPath("git")
		if lookErr != nil {
			return nil, fmt.Errorf("gitserve: locate git executable: %w", lookErr)
		}
		h.gitPath = gitPath
	}
	return h, nil
}

var safeSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// smart-HTTP endpoints handled by git http-backend that this handler exposes.
// The dumb protocol (loose object and packfile GETs) is deliberately not
// served: every modern git client speaks smart HTTP.
var allowedEndpoints = map[string]bool{
	"/info/refs":        true,
	"/git-upload-pack":  true,
	"/git-receive-pack": true,
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	repo, endpoint, ok := h.splitRepoPath(r.URL.Path)
	if !ok || !allowedEndpoints[endpoint] {
		http.NotFound(w, r)
		return
	}
	repoDir, ok := h.resolveRepoDir(repo)
	if !ok {
		http.NotFound(w, r)
		return
	}
	actor := ""
	if h.auth != nil {
		who, allowed := h.auth(r)
		if !allowed {
			w.Header().Set("WWW-Authenticate", `Basic realm="gitserve"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		actor = who
	}
	receive := endpoint == "/git-receive-pack" ||
		(endpoint == "/info/refs" && r.URL.Query().Get("service") == "git-receive-pack")
	if h.readOnly && receive {
		h.rejectReadOnly(w, r, endpoint)
		return
	}

	var before map[string]string
	notify := receive && r.Method == http.MethodPost && h.hook != nil
	if notify {
		snap, err := h.refSnapshot(r, repoDir)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		before = snap
	}

	cleanup, err := deChunkBody(r)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer cleanup()

	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	h.cgiHandler(actor, r).ServeHTTP(rec, r)

	if notify && rec.status < 400 {
		after, err := h.refSnapshot(r, repoDir)
		if err != nil {
			return // response already sent; nothing safe to report
		}
		if updates := diffRefs(before, after); len(updates) > 0 {
			h.hook.OnRefsChanged(repo, updates)
		}
	}
}

// splitRepoPath validates a URL path of the form
// /<segments...>/<name>.git/<endpoint> and returns the repository path
// relative to the root (slash-separated) plus the endpoint suffix. Every
// segment must be a safe path component, which structurally excludes
// traversal (".." is not a safe segment, and absolute or empty segments are
// rejected).
func (h *Handler) splitRepoPath(urlPath string) (repo, endpoint string, ok bool) {
	if !strings.HasPrefix(urlPath, "/") {
		return "", "", false
	}
	segments := strings.Split(strings.TrimPrefix(urlPath, "/"), "/")
	for i, segment := range segments {
		if !safeSegment.MatchString(segment) {
			return "", "", false
		}
		if strings.HasSuffix(segment, ".git") {
			repo = path.Join(segments[:i+1]...)
			endpoint = "/" + path.Join(segments[i+1:]...)
			if endpoint == "/" {
				return "", "", false
			}
			return repo, endpoint, true
		}
	}
	return "", "", false
}

// resolveRepoDir maps a validated repository path to its on-disk directory,
// re-checking after symlink resolution that the directory is still inside the
// serving root. Segment validation alone cannot stop a symlink planted under
// the root from pointing at a repository elsewhere on the filesystem; symlinks
// that resolve to another location under the root remain servable.
func (h *Handler) resolveRepoDir(repo string) (string, bool) {
	repoDir := filepath.Join(h.root, filepath.FromSlash(repo))
	resolved, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(h.root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if !isBareRepo(resolved) {
		return "", false
	}
	return resolved, true
}

// deChunkBody spools a chunked request body to a temporary file and rewrites
// the request to carry a plain Content-Length. The net/http/cgi bridge
// hard-rejects Transfer-Encoding: chunked with HTTP 400, and git switches to
// chunked transfer for push bodies larger than http.postBuffer (1 MiB by
// default), so without this any non-trivial push would fail. The returned
// cleanup must be called after the request has been served; it is a no-op
// when the body needed no spooling. The body is spooled verbatim, so a
// Content-Encoding: gzip push stays compressed for http-backend to inflate.
func deChunkBody(r *http.Request) (cleanup func(), err error) {
	if len(r.TransferEncoding) == 0 && r.ContentLength >= 0 {
		return func() {}, nil
	}
	tmp, err := os.CreateTemp("", "gitserve-body-*")
	if err != nil {
		return nil, fmt.Errorf("gitserve: spool request body: %w", err)
	}
	cleanup = func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}
	size, err := io.Copy(tmp, r.Body)
	if err == nil {
		_, err = tmp.Seek(0, io.SeekStart)
	}
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("gitserve: spool request body: %w", err)
	}
	r.Body = tmp
	r.ContentLength = size
	r.TransferEncoding = nil
	return cleanup, nil
}

func (h *Handler) cgiHandler(actor string, r *http.Request) *cgi.Handler {
	env := []string{
		"GIT_PROJECT_ROOT=" + h.root,
		"GIT_HTTP_EXPORT_ALL=1",
	}
	if actor != "" {
		env = append(env, "REMOTE_USER="+actor)
	}
	if proto := r.Header.Get("Git-Protocol"); proto != "" {
		env = append(env, "GIT_PROTOCOL="+proto)
	}
	receivePack := "false"
	if !h.readOnly {
		receivePack = "true"
	}
	return &cgi.Handler{
		Path: h.gitPath,
		// http.receivepack is set explicitly: http-backend's default only
		// enables receive-pack for authenticated requests, and the auth seam
		// here may legitimately produce anonymous pushes on trusted binds.
		Args:       []string{"-c", "http.receivepack=" + receivePack, "http-backend"},
		Dir:        h.root,
		Env:        env,
		InheritEnv: []string{"PATH", "HOME", "TMPDIR", "GIT_EXEC_PATH", "XDG_CONFIG_HOME"},
	}
}

// rejectReadOnly refuses receive-pack traffic. The ref-advertisement request
// gets a well-formed smart-HTTP service announcement followed by a pkt-line
// ERR packet so the git client reports "remote error: repository is
// read-only" instead of a bare HTTP failure; a direct receive-pack POST
// (which a well-behaved client never sends after that) gets 403.
func (h *Handler) rejectReadOnly(w http.ResponseWriter, r *http.Request, endpoint string) {
	if endpoint == "/info/refs" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
		w.Header().Set("Cache-Control", "no-cache")
		writePktLine(w, "# service=git-receive-pack\n")
		fmt.Fprint(w, "0000") // flush-pkt terminating the service announcement
		writePktLine(w, "ERR repository is read-only\n")
		return
	}
	http.Error(w, "repository is read-only", http.StatusForbidden)
}

func writePktLine(w http.ResponseWriter, line string) {
	fmt.Fprintf(w, "%04x%s", len(line)+4, line)
}

func (h *Handler) refSnapshot(r *http.Request, repoDir string) (map[string]string, error) {
	out, err := gitOutput(r.Context(), h.gitPath, repoDir, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		oid, ref, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		refs[ref] = oid
	}
	return refs, nil
}

func diffRefs(before, after map[string]string) []RefUpdate {
	var updates []RefUpdate
	for ref, newOID := range after {
		oldOID, existed := before[ref]
		switch {
		case !existed:
			updates = append(updates, RefUpdate{Ref: ref, Old: zeroOID(len(newOID)), New: newOID})
		case oldOID != newOID:
			updates = append(updates, RefUpdate{Ref: ref, Old: oldOID, New: newOID})
		}
	}
	for ref, oldOID := range before {
		if _, exists := after[ref]; !exists {
			updates = append(updates, RefUpdate{Ref: ref, Old: oldOID, New: zeroOID(len(oldOID))})
		}
	}
	return updates
}

func zeroOID(length int) string {
	if length <= 0 {
		length = 40
	}
	return strings.Repeat("0", length)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}
