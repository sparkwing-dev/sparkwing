package web

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/docsweb"
)

// Pages serves the dashboard: the static export in bundleFS, rooted at its
// index.html, and the embedded documentation under /docs. Every HTML page it
// renders carries the CSP nonce [SecurityHeaders] put on the request, so wrap
// it in that middleware. A nil bundleFS answers every page with the reason
// this binary carries no dashboard.
func Pages(bundleFS fs.FS) http.Handler {
	mux := http.NewServeMux()
	// safety: ServeMux matches "GET /docs" on that exact path, so the trailing-slash
	// spelling any proxy may normalize to needs its own pattern or the app shell
	// answers it.
	docsHandler := docsweb.Handler()
	mux.Handle("GET /docs", docsHandler)
	mux.Handle("GET /docs/{$}", docsHandler)
	// safety: the app shell would answer /docs/anything with a 200 carrying no
	// documentation. Pages are addressed by ?p=, so nothing lives here.
	mux.Handle("GET /docs/{rest...}", http.NotFoundHandler())
	if bundleFS == nil {
		mux.Handle("GET /", http.HandlerFunc(missingBundle))
	} else {
		mux.Handle("GET /", spaHandler(bundleFS))
	}
	return mux
}

// PublicAsset reports whether r asks for a part of the bundle that carries no
// run data and is served signed out: a favicon, or an immutable build asset
// that exists in bundleFS.
func PublicAsset(r *http.Request, bundleFS fs.FS) bool {
	if bundleFS == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	p := r.URL.Path
	switch {
	case p == "/favicon.ico" || p == "/favicon-orange.ico":
	case strings.HasPrefix(p, "/_next/static/") && path.Clean(p) == p && !strings.Contains(p, `\`):
	default:
		return false
	}
	info, err := fs.Stat(bundleFS, strings.TrimPrefix(p, "/"))
	return err == nil && !info.IsDir()
}

func missingBundle(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, missingBundleMessage, http.StatusServiceUnavailable)
}

func spaHandler(bundleFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(bundleFS))
	assets := &gzipCache{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		p = strings.TrimSuffix(p, "/")
		if p == "favicon.ico" && strings.EqualFold(r.Host, "console.sparkwing.dev") {
			p = "favicon-orange.ico"
			r = r.Clone(r.Context())
			r.URL.Path = "/favicon-orange.ico"
		}
		if p == "" {
			serveTemplatedHTML(w, r, bundleFS, "index.html")
			return
		}

		if strings.HasSuffix(p, ".html") && isTemplatedPath(p) {
			serveTemplatedHTML(w, r, bundleFS, p)
			return
		}

		// hack: prefer <route>.html because Next 16 emits a same-named Turbopack
		// directory that FileServer redirects into a dead end.
		if _, err := fs.Stat(bundleFS, p+".html"); err == nil {
			serveTemplatedHTML(w, r, bundleFS, p+".html")
			return
		}

		if _, err := fs.Stat(bundleFS, p+"/index.html"); err == nil {
			serveTemplatedHTML(w, r, bundleFS, p+"/index.html")
			return
		}

		if info, err := fs.Stat(bundleFS, p); err == nil && !info.IsDir() {
			if serveBundleAsset(w, r, bundleFS, p, assets) {
				return
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		serveTemplatedHTML(w, r, bundleFS, "index.html")
	})
}

func isTemplatedPath(p string) bool {
	return !strings.HasPrefix(p, "_next/") && !strings.HasPrefix(p, "next-dev/")
}

func serveTemplatedHTML(w http.ResponseWriter, r *http.Request, bundleFS fs.FS, name string) {
	raw, err := fs.ReadFile(bundleFS, name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	body := raw
	if strings.EqualFold(r.Host, "console.sparkwing.dev") {
		body = bytes.ReplaceAll(body, []byte("/favicon.ico"), []byte("/favicon-orange.ico"))
	}
	if nonce := cspNonceFrom(r.Context()); nonce != "" {
		body = nonceInlineScripts(body, nonce)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeGeneratedHTML(w, r, body)
}

// safety: every inline script needs the nonce or the CSP blanks the page, so
// the scan follows tag syntax instead of one literal spelling of the tag.
func nonceInlineScripts(raw []byte, nonce string) []byte {
	attr := []byte(` nonce="` + nonce + `"`)
	out := make([]byte, 0, len(raw)+len(attr))
	for i := 0; i < len(raw); {
		open := bytes.IndexByte(raw[i:], '<')
		if open < 0 {
			return append(out, raw[i:]...)
		}
		open += i
		out = append(out, raw[i:open+1]...)
		i = open + 1
		if !opensScriptTag(raw, open) {
			continue
		}
		end := tagEnd(raw, open)
		if end < 0 {
			continue
		}
		tag := raw[open+1 : end]
		if !tagHasAttr(tag, "src") {
			cut := len(tag)
			for cut > 0 && (asciiSpace(tag[cut-1]) || tag[cut-1] == '/') {
				cut--
			}
			out = append(out, tag[:cut]...)
			out = append(out, attr...)
			tag = tag[cut:]
		}
		out = append(out, tag...)
		out = append(out, '>')
		i = end + 1
	}
	return out
}

func opensScriptTag(raw []byte, open int) bool {
	name := open + 1 + len("script")
	if name > len(raw) || !strings.EqualFold(string(raw[open+1:name]), "script") {
		return false
	}
	return name == len(raw) || asciiSpace(raw[name]) || raw[name] == '>' || raw[name] == '/'
}

func tagEnd(raw []byte, open int) int {
	var quote byte
	for i := open + 1; i < len(raw); i++ {
		switch c := raw[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i
		}
	}
	return -1
}

func tagHasAttr(tag []byte, name string) bool {
	i := 0
	for i < len(tag) && !asciiSpace(tag[i]) {
		i++
	}
	for i < len(tag) {
		for i < len(tag) && (asciiSpace(tag[i]) || tag[i] == '/') {
			i++
		}
		start := i
		for i < len(tag) && !asciiSpace(tag[i]) && tag[i] != '=' && tag[i] != '/' {
			i++
		}
		if i == start {
			return false
		}
		if strings.EqualFold(string(tag[start:i]), name) {
			return true
		}
		for i < len(tag) && asciiSpace(tag[i]) {
			i++
		}
		if i == len(tag) || tag[i] != '=' {
			continue
		}
		i++
		for i < len(tag) && asciiSpace(tag[i]) {
			i++
		}
		if i < len(tag) && (tag[i] == '"' || tag[i] == '\'') {
			quote := tag[i]
			i++
			for i < len(tag) && tag[i] != quote {
				i++
			}
			i++
			continue
		}
		for i < len(tag) && !asciiSpace(tag[i]) {
			i++
		}
	}
	return false
}

func asciiSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// RuntimeConfig serves the script the dashboard loads before its bundle:
// the version its navigation shows and whether the page signs in with a
// session cookie, which decides whether its writes carry a CSRF header.
func RuntimeConfig(version string, signIn bool) http.Handler {
	body := "window.__SPARKWING_VERSION__=" + jsStringLiteral(version) + ";\n" +
		"window.__SPARKWING_REQUIRE_LOGIN__=" + jsStringLiteral(strconv.FormatBool(signIn)) + ";\n"
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		// safety: the status is already sent, so a client that went away mid-body is the only failure left to see.
		if _, err := io.WriteString(w, body); err != nil {
			return
		}
	})
}

// safety: encoding/json escapes <, > and &, so the literal cannot close a
// script element; the two JavaScript line terminators are escaped here.
func jsStringLiteral(v string) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	literal := string(encoded)
	literal = strings.ReplaceAll(literal, " ", ` `)
	return strings.ReplaceAll(literal, " ", ` `)
}
