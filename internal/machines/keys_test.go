package machines

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func newKeyLine(t *testing.T, comment string) (string, ssh.PublicKey) {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	pk, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))) + " " + comment, pk
}

func TestParseKey(t *testing.T) {
	good, _ := newKeyLine(t, "me@laptop")
	if _, c, err := ParseKey(good); err != nil || c != "me@laptop" {
		t.Errorf("good key = %v, comment %q", err, c)
	}
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	smallPK, _ := ssh.NewPublicKey(&small.PublicKey)
	for name, line := range map[string]string{
		"options":   `command="/bin/sh" ` + good,
		"two lines": good + "\n" + good,
		"garbage":   "ssh-ed25519 notbase64",
		"rsa-1024":  string(ssh.MarshalAuthorizedKey(smallPK)),
		"empty":     "",
	} {
		if _, _, err := ParseKey(line); status(err) != http.StatusBadRequest {
			t.Errorf("%s: err = %v, want 400", name, err)
		}
	}
}

func TestKeys(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	line, pk := newKeyLine(t, "laptop")
	k, err := s.AddKey(ctx, "alice", line, "")
	if err != nil || k.Label != "laptop" || k.Type != ssh.KeyAlgoED25519 || k.Fingerprint != ssh.FingerprintSHA256(pk) {
		t.Fatalf("add = %+v, %v", k, err)
	}
	if _, err := s.AddKey(ctx, "bob", line, "mine now"); status(err) != http.StatusConflict {
		t.Errorf("bob registering alice's key = %v, want 409", err)
	}

	if err := s.DeleteKey(ctx, "bob", k.ID); status(err) != http.StatusNotFound {
		t.Errorf("bob deleting alice's key = %v, want 404", err)
	}
	if keys, _ := s.Keys(ctx, "bob"); len(keys) != 0 {
		t.Errorf("bob sees %v", keys)
	}
	if err := s.DeleteKey(ctx, "alice", k.ID); err != nil {
		t.Errorf("alice delete = %v", err)
	}

}

func TestKeyCap(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	for i := 0; i < MaxKeys; i++ {
		line, _ := newKeyLine(t, "")
		if _, err := s.AddKey(ctx, "alice", line, ""); err != nil {
			t.Fatal(err)
		}
	}
	line, _ := newKeyLine(t, "")
	if _, err := s.AddKey(ctx, "alice", line, ""); status(err) != http.StatusForbidden {
		t.Errorf("key %d = %v, want 403", MaxKeys+1, err)
	}
}

func TestLongLabel(t *testing.T) {
	s, _ := testService(t)
	line, _ := newKeyLine(t, strings.Repeat("c", 80))
	if k, err := s.AddKey(context.Background(), "alice", line, ""); err != nil || len([]rune(k.Label)) != 63 {
		t.Errorf("a long comment as the label = %q, %v; want it shortened to 63", k.Label, err)
	}
	line2, _ := newKeyLine(t, "")
	if _, err := s.AddKey(context.Background(), "alice", line2, strings.Repeat("x", 64)); status(err) != http.StatusBadRequest {
		t.Errorf("a 64-character label = %v, want 400", err)
	}
}
