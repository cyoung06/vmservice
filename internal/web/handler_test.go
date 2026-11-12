package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	files := fstest.MapFS{
		"index.html":               {Data: []byte(`<html><script>self.a=1</script></html>`)},
		"machine/index.html":       {Data: []byte(`<html>machine</html>`)},
		"404.html":                 {Data: []byte(`<html>missing</html>`)},
		"_next/static/chunks/a.js": {Data: []byte(`console.log(1)`)},
		".hidden":                  {Data: []byte(`secret`)},
	}
	h, err := Handler(files, Config{Title: "VMService"}, "https://vm.example.org")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPages(t *testing.T) {
	h := testHandler(t)
	for _, c := range []struct {
		target, body string
		status       int
	}{
		{"/", "self.a=1", 200},
		{"/machine/?id=vm-a", "machine", 200},
		{"/machine?id=vm-a", "", 301},
		{"/%2Fevil.com%2F..%2Fmachine?id=vm-a", "", 301},
		{"/%2Fmachine?id=vm-a", "", 301},
		{"/config.json", `"title":"VMService"`, 200},
		{"/favicon.svg", "<svg", 200},
		{"/.hidden", "missing", 404},
		{"/nope", "missing", 404},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", c.target, nil))
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.body) {
			t.Errorf("%s = %d %q", c.target, w.Code, w.Body.String())
		}
		if c.status == 301 && w.Header().Get("Location") != "/machine/?id=vm-a" {
			t.Errorf("%s redirects to %s", c.target, w.Header().Get("Location"))
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"connect-src 'self' wss://vm.example.org", "script-src 'self' 'sha256-", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
}
