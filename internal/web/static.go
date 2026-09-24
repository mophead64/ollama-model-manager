package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// staticHashes maps each embedded static file (e.g. "logo-64.png") to a short
// hash of its contents. Embedded files have no modtime, so without this the
// file server sends no validators and browsers re-download every asset on
// every page load, which shows as the logo and favicon flickering on slow links.
var staticHashes = hashStatic()

func hashStatic() map[string]string {
	hashes := map[string]string{}
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(staticFS, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		hashes[strings.TrimPrefix(p, "static/")] = hex.EncodeToString(sum[:])[:12]
		return nil
	})
	if err != nil {
		panic("hash static files: " + err.Error())
	}
	return hashes
}

// staticURL is the "static" template func: the URL for a static file, with its
// content hash as a cache-busting query so it can be cached indefinitely.
func staticURL(name string) string {
	u := "/static/" + name
	if h, ok := staticHashes[name]; ok {
		u += "?v=" + h
	}
	return u
}

// navLogo is the nav logo as a data: URL. Inlined rather than linked because
// each nav link is a full page load, and a linked image, even when cached,
// comes back from the browser's cache after the first paint, so the logo
// blinks out on every navigation.
var navLogo = func() template.URL {
	b, err := fs.ReadFile(staticFS, "static/logo-64.png")
	if err != nil {
		panic("read nav logo: " + err.Error())
	}
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(b))
}()

// staticHandler serves the embedded static files with an ETag, and, for
// URLs carrying the current content hash, as immutable for a year.
func staticHandler() http.Handler {
	files := http.FileServerFS(staticFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := staticHashes[strings.TrimPrefix(path.Clean(r.URL.Path), "/static/")]; ok {
			w.Header().Set("ETag", `"`+h+`"`)
			if r.URL.Query().Get("v") == h {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
		}
		files.ServeHTTP(w, r)
	})
}
