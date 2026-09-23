package web

import (
	"bytes"
	"html/template"
	"net/http"
)

// flowPage is the small page a redirect flow shows between its steps, or
// when it stops.
type flowPage struct {
	Title       string
	Message     string
	Refresh     string
	ActionHref  string
	ActionLabel string
}

var flowPageTmpl = template.Must(template.New("flow").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  {{if .Refresh}}<meta http-equiv="refresh" content="0;url={{.Refresh}}">{{end}}
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>{{.Title}}</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif; background: #0b0e14; color: #c9d1d9; margin: 0; display: flex; min-height: 100vh; align-items: center; justify-content: center; }
    .card { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 2rem 2.5rem; width: 100%; max-width: 440px; box-sizing: border-box; }
    h1 { font-size: 1.15rem; margin: 0 0 1rem 0; font-weight: 600; }
    p { font-size: 0.9rem; line-height: 1.45; margin: 0 0 1.25rem 0; }
    a { display: inline-block; padding: 0.5rem 0.9rem; background: #238636; color: white; border-radius: 4px; text-decoration: none; font-size: 0.9rem; }
    a:hover { background: #2ea043; }
  </style>
</head>
<body>
  <main class="card">
    <h1>{{.Title}}</h1>
    <p>{{.Message}}</p>
    {{if .ActionHref}}<a href="{{.ActionHref}}">{{.ActionLabel}}</a>{{end}}
  </main>
</body>
</html>
`))

func renderFlowPage(w http.ResponseWriter, status int, page flowPage) {
	var body bytes.Buffer
	if err := flowPageTmpl.Execute(&body, page); err != nil {
		http.Error(w, page.Title+": "+page.Message, status)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// safety: a client that went away mid-page starts the connection again.
	if _, err := body.WriteTo(w); err != nil {
		return
	}
}
