package bastion

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/cyoung06/vmservice/internal/limit"
)

type fakeMachines struct {
	owners map[string]string // key id (marshal) -> owner
	valid  time.Duration     // how long a sign-in is honoured
}

func (f fakeMachines) KeyOwner(_ context.Context, k ssh.PublicKey) (string, time.Time, bool) {
	o, ok := f.owners[string(k.Marshal())]
	return o, time.Now().Add(f.valid), ok
}

func (f fakeMachines) Address(_ context.Context, owner, id string) (string, error) {
	if owner == "alice" && id == "vm-aaaaaa" {
		return "127.0.0.1", nil
	}
	return "", errors.New("No such machine.")
}

func signer(t *testing.T) ssh.Signer {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func setup(t *testing.T, max int64) (addr string, alice, stranger ssh.Signer) {
	return setupValid(t, max, time.Hour)
}

func setupValid(t *testing.T, max int64, valid time.Duration) (addr string, alice, stranger ssh.Signer) {
	t.Helper()
	// An "sshd" on the machine: echoes.
	echo, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { echo.Close() })
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(hostPriv, "")
	hostFile := filepath.Join(t.TempDir(), "host")
	os.WriteFile(hostFile, pem.EncodeToMemory(block), 0o600)
	alice, stranger = signer(t), signer(t)
	b, err := New(hostFile, fakeMachines{owners: map[string]string{string(alice.PublicKey().Marshal()): "alice"}, valid: valid}, &limit.PerOwner{Max: max}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(echo.Addr().String())
	b.dialPort, _ = strconv.Atoi(port)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go b.Serve(ctx, l)
	return l.Addr().String(), alice, stranger
}

func dial(addr string, s ssh.Signer) (*ssh.Client, error) {
	return ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "anyone", Auth: []ssh.AuthMethod{ssh.PublicKeys(s)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
}

func TestForwardToOwnMachineOnly(t *testing.T) {
	addr, alice, stranger := setup(t, 8)
	if _, err := dial(addr, stranger); err == nil {
		t.Fatal("an unregistered key signed in")
	}
	c, err := dial(addr, alice)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	conn, err := c.Dial("tcp", "vm-aaaaaa:22")
	if err != nil {
		t.Fatalf("forward to own machine: %v", err)
	}
	conn.Write([]byte("SSH-2.0-hello\r\n"))
	buf := make([]byte, 15)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "SSH-2.0-hello\r\n" {
		t.Errorf("echo = %q, %v", buf, err)
	}
	conn.Close()
	for _, target := range []string{"vm-bbbbbb:22", "vm-aaaaaa:80", "127.0.0.1:22", "10.0.0.1:22"} {
		if conn, err := c.Dial("tcp", target); err == nil {
			conn.Close()
			t.Errorf("forward to %s was allowed", target)
		}
	}
	if _, _, err := c.OpenChannel("session", nil); err == nil {
		t.Error("a shell session was opened")
	}
	if _, err := c.Listen("tcp", "127.0.0.1:0"); err == nil {
		t.Error("remote forwarding was allowed")
	}
}

func TestConnectionCap(t *testing.T) {
	addr, alice, _ := setup(t, 1)
	c1, err := dial(addr, alice)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	c2, err := dial(addr, alice)
	if err != nil {
		return // refused outright: fine
	}
	defer c2.Close()
	if _, err := c2.Dial("tcp", "vm-aaaaaa:22"); err == nil {
		t.Error("a second connection over the cap of 1 could forward")
	}
}

// The session ends when the owner's sign-in window does, open forwards included.
func TestSessionEndsWithSignInWindow(t *testing.T) {
	addr, alice, _ := setupValid(t, 8, 300*time.Millisecond)
	c, err := dial(addr, alice)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	conn, err := c.Dial("tcp", "vm-aaaaaa:22")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Error("the forward still works after the sign-in window closed")
	}
	if err := c.Wait(); err == nil {
		t.Error("the session didn't end")
	}
}
