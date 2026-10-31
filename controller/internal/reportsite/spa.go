package reportsite

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// webUI holds the single page app that `make ui-build` writes to web/dist/,
// and the placeholder page served when it was not built. The pattern is the
// whole web/ tree, because web/dist/ is generated and gitignored: embedding it
// directly would fail in a checkout that never ran the UI build.
//
//go:embed all:web
var webUI embed.FS

// SPACSP is the Content-Security-Policy of the single page app's index and assets.
const SPACSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

const (
	distDir         = "web/dist"
	placeholderFile = "web/ui-placeholder.html"
	cacheImmutable  = "public, max-age=31536000, immutable"
)

// spa serves the built UI: hashed files under /assets/ are immutable, other
// files at the root of dist/ (favicon) are revalidated, and every other path
// gets index.html with no-store, so the app's router handles it.
type spa struct {
	files fs.FS
	index []byte
	// built reports whether index is the built app rather than the placeholder.
	built bool
}

// newSPA reads the embedded build, or the placeholder when there is none.
func newSPA() (*spa, error) {
	files, err := fs.Sub(webUI, distDir)
	if err != nil {
		return nil, fmt.Errorf("opening the embedded UI: %w", err)
	}
	if index, err := fs.ReadFile(files, "index.html"); err == nil {
		return &spa{files: files, index: index, built: true}, nil
	}
	index, err := fs.ReadFile(webUI, placeholderFile)
	if err != nil {
		return nil, fmt.Errorf("reading the UI placeholder: %w", err)
	}
	return &spa{files: files, index: index}, nil
}

// asset serves GET /assets/{path...}. A missing asset is a plain 404, never
// index.html, so a stale script tag cannot load HTML as JavaScript.
func (a *spa) asset(w http.ResponseWriter, r *http.Request) {
	name := path.Clean("assets/" + r.PathValue("path"))
	if !strings.HasPrefix(name, "assets/") || !a.serveFile(w, r, name, cacheImmutable) {
		http.NotFound(w, r)
	}
}

// page serves every other GET path: a root file of dist/ when one matches,
// else index.html.
func (a *spa) page(w http.ResponseWriter, r *http.Request) {
	if name := strings.TrimPrefix(path.Clean(r.URL.Path), "/"); name != "" && name != "index.html" && !strings.Contains(name, "/") {
		if a.serveFile(w, r, name, "no-cache") {
			return
		}
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", SPACSP)
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(a.index))
}

// serveFile writes one regular file of dist/ and reports whether it existed.
func (a *spa) serveFile(w http.ResponseWriter, r *http.Request, name, cacheControl string) bool {
	info, err := fs.Stat(a.files, name)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := fs.ReadFile(a.files, name)
	if err != nil {
		return false
	}
	h := w.Header()
	h.Set("Cache-Control", cacheControl)
	h.Set("Content-Security-Policy", SPACSP)
	// ServeContent sets Content-Type from the extension.
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	return true
}
