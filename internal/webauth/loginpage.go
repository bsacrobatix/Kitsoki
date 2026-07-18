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
  a.button { display: inline-block; background: #1b1f24; color: #fff; text-decoration: none;
             padding: 0.7rem 1.4rem; border-radius: 6px; font-size: 0.95rem; }
  a.button:hover { background: #32383f; }
</style>
</head>
<body>
<div class="card">
  <h1>Sign in</h1>
  {{if .Error}}<p class="err">{{.Error}}</p>{{end}}
  {{if .Invited}}<p class="invited">You have been invited. Sign in with GitHub to accept.</p>{{end}}
  <a class="button" href="{{.StartURL}}">Sign in with GitHub</a>
</div>
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
	StartURL string
	Invited  bool
	Error    string
}

func renderLoginPage(w http.ResponseWriter, data loginPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = loginPageTmpl.Execute(w, data)
}

func renderMessagePage(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = messagePageTmpl.Execute(w, msg)
}
