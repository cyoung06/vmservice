// Package web serves the UI: the Next.js export of web/, embedded, at /, and /config.json.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

// The export (web/out) is copied into ui/ here before the binary is built (see ui/.gitignore and
// web/scripts/embed.mjs). A build without it serves no pages.
//
//go:embed all:ui
var embedded embed.FS

// UI is the embedded export.
func UI() fs.FS {
	files, _ := fs.Sub(embedded, "ui")
	return files
}

// Config is /config.json: what the UI is told about its deployment.
type Config struct {
	Title         string `json:"title"`
	LoginProvider string `json:"loginProvider"`
}

// basePolicy is the Content-Security-Policy of every response: everything from this origin. The
// consoles' websockets are this origin too (wsOrigin, since older browsers don't count ws: as
// 'self'). Styles allow inline: the terminal and the VNC viewer set their own.
func basePolicy(wsOrigin string) string {
	return "default-src 'self'; connect-src 'self' " + wsOrigin + "; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
}

var inlineScript = regexp.MustCompile(`(?s)<script(\s[^>]*)?>(.*?)</script>`)

// pagePolicy adds the script-src that lets a page run its own inline scripts and nothing else
// inline: 'self' plus the sha256 of each one.
func pagePolicy(base string, page []byte) string {
	p := base + "; script-src 'self'"
	for _, m := range inlineScript.FindAllSubmatch(page, -1) {
		if len(m[2]) > 0 {
			sum := sha256.Sum256(m[2])
			p += " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		}
	}
	return p
}

// Handler serves the UI at /, /config.json and /healthz. A page is served at its directory
// (/machine/ is machine/index.html); anything that isn't a file of the export is the export's 404
// page. routes adds the API and sign-in to the same mux. origin is the public origin (no trailing
// slash).
func Handler(files fs.FS, cfg Config, origin string, routes ...func(*http.ServeMux)) (http.Handler, error) {
	wsOrigin := strings.Replace(strings.Replace(origin, "https://", "wss://", 1), "http://", "ws://", 1)
	base := basePolicy(wsOrigin)
	policies := map[string]string{} // .html file -> its Content-Security-Policy
	err := fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".html") {
			return err
		}
		page, err := fs.ReadFile(files, name)
		policies[name] = pagePolicy(base, page)
		return err
	})
	if err != nil {
		return nil, err
	}
	config, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	notFound := func(w http.ResponseWriter) {
		page, err := fs.ReadFile(files, "404.html")
		if err != nil {
			http.Error(w, "404 page not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Security-Policy", policies["404.html"])
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		w.Write(page)
	}

	mux := http.NewServeMux()
	for _, add := range routes {
		add(mux)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /config.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(config)
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || strings.HasSuffix(r.URL.Path, "/") {
			name = path.Join(name, "index.html")
		}
		if strings.HasPrefix(name, ".") || strings.Contains(name, "/.") {
			notFound(w)
			return
		}
		info, err := fs.Stat(files, name)
		if err == nil && info.IsDir() {
			// A page without its trailing slash: send the browser to the canonical address, built
			// from the cleaned name, never the raw path: "/%2Fevil.com%2F..%2Fmachine" must not
			// become "//evil.com/../machine/".
			target := "/" + name + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		}
		if err != nil {
			notFound(w)
			return
		}
		if p, page := policies[name]; page {
			w.Header().Set("Content-Security-Policy", p)
			w.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(name, "_next/static/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeFileFS(w, r, files, name)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", base)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		mux.ServeHTTP(w, r)
	}), nil
}
