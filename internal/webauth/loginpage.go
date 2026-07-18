package webauth

import (
	"html/template"
	"net/http"
)

// The login and message pages are deliberately unbranded, self-contained
// HTML: no external assets, no SPA involvement, nothing to build.
var loginPageTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in</title>
<style>
  body { margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
         background: #f6f7f8; font-family: system-ui, sans-serif; color: #1b1f24; }
  .card { background: #fff; border: 1px solid #d7dbe0; border-radius: 8px; padding: 2.5rem 3rem; text-align: center; }
  h1 { font-size: 1.1rem; font-weight: 600; margin: 0 0 1.5rem; }
  .err { color: #b42318; font-size: 0.85rem; margin: 0 0 1rem; max-width: 22rem; }
  .invited { color: #3d454e; font-size: 0.85rem; margin: 0 0 1rem; max-width: 22rem; }
  a.button, button { display: inline-block; background: #1b1f24; color: #fff; text-decoration: none;
             padding: 0.7rem 1.4rem; border-radius: 6px; font-size: 0.95rem; }
  button { border: 0; cursor: pointer; font-family: inherit; }
  a.button:hover, button:hover { background: #32383f; }
</style>
</head>
<body>
<div class="card">
  <h1>Sign in</h1>
  {{if .Error}}<p class="err">{{.Error}}</p>{{end}}
  {{if .Invited}}<p class="invited">You have been invited. Sign in with GitHub to accept.</p>{{end}}
  {{if .DeviceFlow}}
  <form method="post" action="{{.StartURL}}"><button type="submit">Sign in with GitHub</button></form>
  {{else}}
  <a class="button" href="{{.StartURL}}">Sign in with GitHub</a>
  {{end}}
</div>
</body>
</html>`))

var devicePageTmpl = template.Must(template.New("device").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Complete GitHub sign-in</title>
<style>
  body { margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
         background: #f6f7f8; font-family: system-ui, sans-serif; color: #1b1f24; }
  .card { background: #fff; border: 1px solid #d7dbe0; border-radius: 8px; padding: 2.5rem 3rem;
          text-align: center; max-width: 28rem; }
  h1 { font-size: 1.1rem; font-weight: 600; margin: 0 0 1rem; }
  p { color: #3d454e; font-size: 0.9rem; line-height: 1.5; }
  code { display: block; margin: 1.25rem 0; font: 700 1.7rem ui-monospace, monospace;
         letter-spacing: 0.12em; color: #1b1f24; }
  a.button { display: inline-block; background: #1b1f24; color: #fff; text-decoration: none;
             padding: 0.7rem 1.4rem; border-radius: 6px; font-size: 0.95rem; }
  #status { min-height: 1.5rem; margin-bottom: 0; }
</style>
</head>
<body>
<div class="card" id="device-login" data-poll-token="{{.PollToken}}" data-poll-interval="{{.PollIntervalMS}}">
  <h1>Complete GitHub sign-in</h1>
  <p>Copy this one-time code, then open GitHub and authorize Kitsoki.</p>
  <code>{{.UserCode}}</code>
  <a class="button" href="{{.VerificationURI}}" target="_blank" rel="noopener noreferrer">Open GitHub</a>
  <p id="status" role="status">Waiting for GitHub authorization…</p>
</div>
<script>
(() => {
  const card = document.getElementById('device-login');
  const status = document.getElementById('status');
  const token = card.dataset.pollToken;
  let delay = Math.max(Number(card.dataset.pollInterval) || 5000, 1000);
  async function poll() {
    try {
      const response = await fetch('/auth/github/device/poll', {
        method: 'POST',
        credentials: 'same-origin',
        headers: {'Accept': 'application/json', 'X-Kitsoki-Device-Token': token}
      });
      const body = await response.json();
      if (response.ok && body.status === 'complete') {
        window.location.assign(body.next || '/');
        return;
      }
      if (response.status === 202 && body.status === 'pending') {
        delay = Math.max(Number(body.retry_after_ms) || delay, 1000);
        window.setTimeout(poll, delay);
        return;
      }
      status.textContent = body.message || 'Sign-in could not be completed. Start again.';
    } catch (_) {
      status.textContent = 'Connection interrupted. Retrying…';
      window.setTimeout(poll, delay);
    }
  }
  window.setTimeout(poll, delay);
})();
</script>
</body>
</html>`))

var messagePageTmpl = template.Must(template.New("message").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in</title>
<style>
  body { margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
         background: #f6f7f8; font-family: system-ui, sans-serif; color: #1b1f24; }
  .card { background: #fff; border: 1px solid #d7dbe0; border-radius: 8px; padding: 2.5rem 3rem;
          text-align: center; max-width: 24rem; }
  p { margin: 0; font-size: 0.95rem; }
</style>
</head>
<body>
<div class="card"><p>{{.}}</p></div>
</body>
</html>`))

type loginPageData struct {
	StartURL   string
	Invited    bool
	Error      string
	DeviceFlow bool
}

type devicePageData struct {
	UserCode        string
	VerificationURI string
	PollToken       string
	PollIntervalMS  int64
}

func renderLoginPage(w http.ResponseWriter, data loginPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = loginPageTmpl.Execute(w, data)
}

func renderDevicePage(w http.ResponseWriter, data devicePageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	_ = devicePageTmpl.Execute(w, data)
}

func renderMessagePage(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = messagePageTmpl.Execute(w, msg)
}
