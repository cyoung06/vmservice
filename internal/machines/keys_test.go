package machines

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"sigs.k8s.io/yaml"

	"github.com/cyoung06/vmservice/internal/store"
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

func status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
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
	// A member who hasn't signed in within the session lifetime: their keys stop working.
	s.store.(*store.Memory).SetSignedIn("alice", time.Now().Add(-2*time.Hour))
	if _, _, ok := lookupFor(s).KeyOwner(ctx, pk); ok {
		t.Error("a key was honoured long after its owner's last sign-in")
	}
	s.store.(*store.Memory).SetSignedIn("alice", time.Now())
	if _, err := s.AddKey(ctx, "bob", line, "mine now"); status(err) != http.StatusConflict {
		t.Errorf("bob registering alice's key = %v, want 409", err)
	}
	if owner, until, ok := lookupFor(s).KeyOwner(ctx, pk); !ok || owner != "alice" || time.Until(until) < 50*time.Minute {
		t.Errorf("owner = %q %v", owner, ok)
	}
	_, other := newKeyLine(t, "")
	if _, _, ok := lookupFor(s).KeyOwner(ctx, other); ok {
		t.Error("an unregistered key has an owner")
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
	if _, _, ok := lookupFor(s).KeyOwner(ctx, pk); ok {
		t.Error("a deleted key still has an owner")
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

func TestCreateInjectsKeys(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	line, pk := newKeyLine(t, "laptop")
	if _, err := s.AddKey(ctx, "alice", line, ""); err != nil {
		t.Fatal(err)
	}
	r := request(t)
	r.Image.CloudConfig = map[string]any{"ssh_authorized_keys": []any{"ssh-ed25519 AAAA-image-backdoor"}}
	m, err := s.Create(ctx, "alice", r)
	if err != nil {
		t.Fatal(err)
	}
	data, err := s.secretUserData(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	yaml.Unmarshal(data, &doc)
	keys, _ := doc["ssh_authorized_keys"].([]any)
	want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
	if len(keys) != 1 || keys[0] != want {
		t.Errorf("ssh_authorized_keys = %v, want only alice's key", keys)
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

func lookupFor(s *Service) *Lookup {
	return &Lookup{Namespace: s.Namespace, SignInValid: time.Hour, store: s.store, read: s.read}
}

// The bastion's address lookup: only the owner's machine, only by a well-formed id.
func TestLookupAddress(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	m, err := s.Create(ctx, "alice", request(t))
	if err != nil {
		t.Fatal(err)
	}
	l := lookupFor(s)
	for _, c := range []struct{ owner, id string }{{"bob", m.ID}, {"alice", "a/../../secrets"}, {"alice", "vm-zzzzzz"}} {
		if _, err := l.Address(ctx, c.owner, c.id); err != ErrNotFound {
			t.Errorf("Address(%s, %q) = %v, want not found", c.owner, c.id, err)
		}
	}
	if _, err := l.Address(ctx, "alice", m.ID); status(err) != http.StatusConflict {
		t.Errorf("Address of a machine with no running instance = %v, want 409", err)
	}
}
