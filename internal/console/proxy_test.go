package console

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

func accept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// fakeVirtAPI answers the upgrade like virt-api does, then echoes every byte back.
func fakeVirtAPI(t *testing.T, seen chan<- *http.Request) *httptest.Server {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		if r.Header.Get("Authorization") != "Bearer rotated-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + accept(r.Header.Get("Sec-WebSocket-Key")) + "\r\nSec-WebSocket-Protocol: " + Subprotocol + "\r\n\r\n")
		rw.Flush()
		io.Copy(conn, rw)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func handshake(t *testing.T, addr, key, cookie string) (net.Conn, *bufio.Reader, *http.Response) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	req := "GET /console HTTP/1.1\r\nHost: " + addr + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Protocol: " + Subprotocol + "\r\nSec-WebSocket-Extensions: permessage-deflate\r\nCookie: " + cookie + "\r\n\r\n"
	conn.Write([]byte(req))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn, br, resp
}

func TestPassThrough(t *testing.T) {
	seen := make(chan *http.Request, 1)
	upstream := fakeVirtAPI(t, seen)
	tokenFile := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tokenFile, []byte("rotated-token\n"), 0o600)
	cfg := &rest.Config{Host: upstream.URL, BearerTokenFile: tokenFile, TLSClientConfig: rest.TLSClientConfig{Insecure: true}}
	p, err := New(cfg, upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsUpgrade(r) {
			http.Error(w, "not an upgrade", http.StatusBadRequest)
			return
		}
		if err := p.Serve(r.Context(), w, r, "/apis/subresources.kubevirt.io/v1/namespaces/ns/virtualmachineinstances/vm-a/console", time.Now().Add(time.Hour)); err != nil {
			t.Error(err)
		}
	}))
	defer front.Close()

	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	conn, br, resp := handshake(t, front.Listener.Addr().String(), key, "vmservice_session=secret")
	defer conn.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != accept(key) || resp.Header.Get("Sec-WebSocket-Protocol") != Subprotocol {
		t.Fatalf("handshake answer = %d %v", resp.StatusCode, resp.Header)
	}
	up := <-seen
	if up.URL.Path != "/apis/subresources.kubevirt.io/v1/namespaces/ns/virtualmachineinstances/vm-a/console" {
		t.Errorf("upstream path = %s", up.URL.Path)
	}
	if up.Header.Get("Cookie") != "" || up.Header.Get("Sec-WebSocket-Extensions") != "" {
		t.Errorf("the browser's cookie or extensions reached the API server: %v", up.Header)
	}
	conn.Write([]byte("hello\r"))
	buf := make([]byte, 6)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "hello\r" {
		t.Errorf("echo = %q, %v", buf, err)
	}
}

func TestUpstreamRefusal(t *testing.T) {
	seen := make(chan *http.Request, 1)
	upstream := fakeVirtAPI(t, seen)
	cfg := &rest.Config{Host: upstream.URL, BearerToken: "wrong", TLSClientConfig: rest.TLSClientConfig{Insecure: true}}
	p, _ := New(cfg, upstream.URL)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := p.Serve(r.Context(), w, r, "/x", time.Now().Add(time.Hour))
		if e, ok := err.(*UpstreamError); !ok || e.Status != http.StatusUnauthorized {
			t.Errorf("err = %v, want the 401 passed on", err)
		}
		http.Error(w, "refused", http.StatusBadGateway)
	}))
	defer front.Close()
	conn, _, resp := handshake(t, front.Listener.Addr().String(), "a2V5", "")
	defer conn.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestSessionExpiryClosesTheConsole(t *testing.T) {
	seen := make(chan *http.Request, 1)
	upstream := fakeVirtAPI(t, seen)
	cfg := &rest.Config{Host: upstream.URL, BearerToken: "rotated-token", TLSClientConfig: rest.TLSClientConfig{Insecure: true}}
	p, _ := New(cfg, upstream.URL)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.Serve(r.Context(), w, r, "/x", time.Now().Add(200*time.Millisecond))
	}))
	defer front.Close()
	conn, br, resp := handshake(t, front.Listener.Addr().String(), "a2V5", "")
	defer conn.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := br.ReadByte(); err != io.EOF && !strings.Contains(err.Error(), "reset") {
		t.Errorf("read after expiry = %v, want the connection closed", err)
	}
}

func TestIsUpgrade(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Key", "k")
	if IsUpgrade(r) {
		t.Error("an upgrade without KubeVirt's subprotocol was accepted")
	}
	r.Header.Set("Sec-WebSocket-Protocol", "binary, "+Subprotocol)
	if !IsUpgrade(r) {
		t.Error("an upgrade offering the subprotocol was refused")
	}
}
