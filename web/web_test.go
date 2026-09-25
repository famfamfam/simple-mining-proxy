package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestNotBuiltExplainsHowToBuild(t *testing.T) {
	w := get(handler(fstest.MapFS{"robots.txt": {Data: []byte("x")}}), "/")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "npm run build") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestCaching(t *testing.T) {
	h := handler(fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html>")},
		"assets/app-1a2b.js": {Data: []byte("x")},
	})
	if w := get(h, "/"); w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("index: %d %v", w.Code, w.Header())
	}
	if w := get(h, "/assets/app-1a2b.js"); w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %v", w.Code, w.Header())
	}
}
