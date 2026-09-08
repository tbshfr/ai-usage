package web

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/tbshfr/ai-usage"
)

var staticFS = func() fs.FS {
	sub, err := fs.Sub(assets.Static, "web/static")
	if err != nil {
		panic(err)
	}
	return sub
}()

// Hash the embedded bytes once. Unchanged assets keep their URLs across
// restarts and releases, including builds that share the version "dev".
var staticHashes = func() map[string]string {
	hashes := make(map[string]string)
	err := fs.WalkDir(staticFS, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(staticFS, name)
		if err != nil {
			return err
		}
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(data))
		return nil
	})
	if err != nil {
		panic(err)
	}
	return hashes
}()

func staticURL(name string) string {
	hash, ok := staticHashes[name]
	if !ok {
		panic("unknown static asset: " + name)
	}
	return "/static/" + name + "?v=" + hash
}

func staticHandler() http.Handler {
	files := http.StripPrefix("/static/", http.FileServerFS(staticFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		if hash, ok := staticHashes[strings.TrimPrefix(r.URL.Path, "/static/")]; ok {
			w.Header().Set("ETag", `"`+hash+`"`)
			// Never give arbitrary or stale version queries a long cache lifetime.
			if r.URL.Query().Get("v") == hash {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
		}
		files.ServeHTTP(w, r)
	})
}
