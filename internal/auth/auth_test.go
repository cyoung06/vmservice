package auth

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testAuth(t *testing.T) *Auth {
	t.Helper()
	key := filepath.Join(t.TempDir(), "key")
	os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))+"\n"), 0o600)
	s, err := newSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	return &Auth{seal: s}
}

func withCookie(name, value string) *http.Request {
	r := httptest.NewRequest("GET", "/api/session", nil)
	r.AddCookie(&http.Cookie{Name: name, Value: value})
	return r
}

func TestSession(t *testing.T) {
	a := testAuth(t)
	owner := OwnerID("https://sso.example.org/realms/club", "f1c0ffee-0000-4000-8000-000000000001")
	good, _ := a.seal.seal(sessionCookie, Session{Owner: owner, Username: "alice", Expires: time.Now().Add(time.Hour)})
	if s, ok := a.Session(withCookie(sessionCookie, good)); !ok || s.Username != "alice" || s.Owner != owner {
		t.Errorf("valid session = %+v, %v", s, ok)
	}
	expired, _ := a.seal.seal(sessionCookie, Session{Owner: owner, Username: "alice", Expires: time.Now().Add(-time.Second)})
	if _, ok := a.Session(withCookie(sessionCookie, expired)); ok {
		t.Error("an expired session was accepted")
	}
	// A login cookie's value can't be replayed as a session (the cookie name is authenticated).
	moved, _ := a.seal.seal(loginCookie, Session{Owner: owner, Username: "alice", Expires: time.Now().Add(time.Hour)})
	if _, ok := a.Session(withCookie(sessionCookie, moved)); ok {
		t.Error("a value sealed for another cookie was accepted")
	}
	tampered := []byte(good)
	tampered[len(tampered)-3] ^= 1
	if _, ok := a.Session(withCookie(sessionCookie, string(tampered))); ok {
		t.Error("a tampered session was accepted")
	}
	bad, _ := a.seal.seal(sessionCookie, Session{Owner: "alice", Username: "alice", Expires: time.Now().Add(time.Hour)})
	if _, ok := a.Session(withCookie(sessionCookie, bad)); ok {
		t.Error("a session with an invalid owner id was accepted")
	}
	if OwnerID("https://a", "x") == OwnerID("https://b", "x") || len(owner) != 40 {
		t.Error("owner ids must differ per issuer and be 40 hex digits")
	}
}

func TestKeyLength(t *testing.T) {
	key := filepath.Join(t.TempDir(), "key")
	os.WriteFile(key, []byte("short"), 0o600)
	if _, err := newSealer(key); err == nil {
		t.Error("a short key was accepted")
	}
	os.WriteFile(key, make([]byte, 32), 0o600)
	if _, err := newSealer(key); err != nil {
		t.Errorf("a raw 32-byte key: %v", err)
	}
}

func TestLocalPath(t *testing.T) {
	for in, want := range map[string]string{
		"":                     "/",
		"/machine/?id=vm-abc":  "/machine/?id=vm-abc",
		"//evil.example":       "/",
		"https://evil.example": "/",
		`/\evil.example`:       "/",
		"machine":              "/",
	} {
		if got := localPath(in); got != want {
			t.Errorf("localPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUserAndGroups(t *testing.T) {
	user, groups := userAndGroups(map[string]any{"preferred_username": "alice", "groups": []any{"/vm-access", "members", 3}}, "preferred_username", "groups")
	if user != "alice" || len(groups) != 2 || groups[0] != "vm-access" || groups[1] != "members" {
		t.Errorf("= %q, %v", user, groups)
	}
	if _, groups := userAndGroups(map[string]any{"groups": "vm-access"}, "u", "groups"); groups != nil {
		t.Errorf("a string groups claim gave %v", groups)
	}
}
