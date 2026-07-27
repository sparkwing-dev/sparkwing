// Package docsweb renders sparkwing's embedded documentation for a browser.
//
// The pages come from pkg/docs, the one embedded set the CLI already reads, so
// the dashboard and `sparkwing docs` describe the same build from the same
// markdown. The two renderings differ: `sparkwing docs read` prints the body
// with cross-page links rewritten into CLI commands, and this one turns those
// links into anchors.
//
// This package only renders; it embeds nothing of its own. It is internal
// rather than part of pkg/ because HTML is a rendering choice, and a renderer
// on the public API is a shape adopters would pin.
package docsweb

import (
	"html/template"
	"net/http"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

// Handler serves the embedded set: the index on whatever path it is mounted
// at, and one page at `?p=<slug>`. Addressing pages by query string rather
// than by path keeps the handler indifferent to its mount point, so the
// dashboard can hang it on /docs without every cross-page link having to know
// that path -- and it keeps the whole surface a single route, which is what
// lets it be registered ahead of the dashboard's catch-all.
//
// An unknown slug is a 404. The catch-all this sits in front of answers every
// path with the dashboard shell, so a docs route that returned 200 for a
// nonsense slug would be indistinguishable from not being registered at all.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		slug := r.URL.Query().Get("p")
		if slug == "" {
			_ = tmpl.ExecuteTemplate(w, "index", docs.List())
			return
		}
		body, err := docs.ReadRaw(slug)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			_ = tmpl.ExecuteTemplate(w, "missing", slug)
			return
		}
		_ = tmpl.ExecuteTemplate(w, "page", map[string]any{
			"Title": titleOf(slug),
			"Body":  renderMarkdown(body, pageHref),
		})
	})
}

// titleOf returns the index title for a slug, falling back to the slug so a
// page whose markdown carries no heading still names itself.
func titleOf(slug string) string {
	for _, e := range docs.List() {
		if e.Slug == slug {
			if e.Title != "" {
				return e.Title
			}
			break
		}
	}
	return slug
}

// pageHref resolves a cross-page markdown link to a query into this handler.
// A link to a slug the set does not carry is reported unknown so the renderer
// leaves the text unlinked rather than publishing a dead link.
func pageHref(slug string) (string, bool) {
	for _, e := range docs.List() {
		if e.Slug == slug {
			return "?p=" + url.QueryEscape(slug), true
		}
	}
	return "", false
}

var tmpl = template.Must(template.New("docs").Parse(pageHTML))

// The page carries its palette inline. A docs view that fetched a stylesheet
// would stop working the moment it is read from a dashboard on a machine with
// no route to the internet, which is the case the embedded set exists for.
const pageHTML = `
{{define "head"}}<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.}} &mdash; sparkwing docs</title>
<meta name="color-scheme" content="dark light">
<style>
:root{
--bg:#0d0f14;--panel:#171a22;--border:rgba(255,255,255,.10);
--text:#e9e7e2;--muted:#98a0ae;--faint:#6b7280;
--accent:#ffb020;--accent-2:#ff7a45;--glow:rgba(255,176,32,.22);
--radius:14px;--radius-sm:10px;
--shadow:0 1px 0 rgba(255,255,255,.03),0 10px 30px -12px rgba(0,0,0,.6);
--font:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif;
--mono:ui-monospace,SFMono-Regular,"SF Mono",Menlo,Consolas,monospace;
}
@media (prefers-color-scheme:light){:root{
--bg:#f6f4ef;--panel:#fff;--border:rgba(30,20,10,.12);
--text:#1c1a16;--muted:#5d616a;--faint:#8b8f98;
--accent:#b26a00;--accent-2:#c2410c;--glow:rgba(178,106,0,.14);
--shadow:0 1px 0 rgba(255,255,255,.6),0 12px 28px -16px rgba(60,40,20,.35);
}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:16px/1.65 var(--font);
background-image:radial-gradient(60vw 40vh at 0% 0%,var(--glow),transparent 60%);
background-attachment:fixed}
header,main{max-width:900px;margin:0 auto;padding:0 20px}
header{display:flex;align-items:center;gap:10px;height:64px}
.brand{display:flex;align-items:center;gap:8px;font-weight:800;font-size:20px;
letter-spacing:.2px;color:var(--text);text-decoration:none}
.brand b{color:var(--accent)}
.tagline{margin-left:auto;font-size:12px;color:var(--faint)}
main{padding-bottom:80px}
h1{font-size:28px;line-height:1.25;margin:24px 0 8px}
h2{font-size:20px;margin:32px 0 8px}
h3{font-size:16px;margin:24px 0 6px}
h4{font-size:15px;margin:20px 0 6px;color:var(--muted)}
p,li,td,th{color:var(--text)}
code{font:13px/1.5 var(--mono);background:var(--panel);
border:1px solid var(--border);border-radius:6px;padding:1px 5px}
pre{background:var(--panel);border:1px solid var(--border);
border-radius:var(--radius-sm);padding:14px 16px;overflow-x:auto}
pre code{background:none;border:0;padding:0;font-size:13px}
a{color:var(--accent)}
.tablewrap,table{display:block;overflow-x:auto;max-width:100%}
table{border-collapse:collapse;margin:16px 0;font-size:14px}
th,td{border:1px solid var(--border);padding:7px 10px;text-align:left;
vertical-align:top}
th{background:var(--panel);font-weight:700}
.card{display:block;background:var(--panel);border:1px solid var(--border);
border-radius:var(--radius);box-shadow:var(--shadow);padding:14px 16px;
margin:10px 0;text-decoration:none}
.card:hover{border-color:var(--accent)}
.card .t{font-weight:700;color:var(--text)}
.card .b{color:var(--muted);font-size:14px;margin-top:4px}
.back{display:inline-block;margin:20px 14px 0 0;color:var(--muted);
text-decoration:none;font-size:14px}
</style></head><body>
<header><a class="brand" href="?">spark<b>wing</b></a>
<span class="tagline">docs &middot; served from this binary</span></header>
<main>{{end}}
{{define "foot"}}</main></body></html>{{end}}

{{define "index"}}{{template "head" "docs"}}
<h1>sparkwing docs</h1>
<p>These pages ship inside the binary serving them, so they describe this
build. The same set is on the command line as <code>sparkwing docs list</code>
and <code>sparkwing docs read --topic &lt;slug&gt;</code>.</p>
{{range .}}<a class="card" href="?p={{.Slug}}">
<div class="t">{{.Title}}</div><div class="b">{{.Summary}}</div></a>{{end}}
<a class="back" href="/">&larr; dashboard</a>
{{template "foot"}}{{end}}

{{define "page"}}{{template "head" .Title}}
{{.Body}}
<a class="back" href="?">&larr; all docs</a>
<a class="back" href="/">&larr; dashboard</a>
{{template "foot"}}{{end}}

{{define "missing"}}{{template "head" "not found"}}
<h1>No such page</h1>
<p>sparkwing has no doc page called <code>{{.}}</code>.</p>
<a class="back" href="?">&larr; all docs</a>
{{template "foot"}}{{end}}
`
