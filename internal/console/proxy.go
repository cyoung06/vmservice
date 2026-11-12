// Package console passes a browser's websocket through to KubeVirt's VMI console or vnc
// subresource. Browsers can't put an Authorization header on a websocket, so the browser is
// authenticated by its session (the caller checks it) and the upstream request carries the
// service account's token instead. After the handshake the bytes are copied both ways untouched:
// the frames are between the browser and virt-api.
package console

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"k8s.io/client-go/rest"
)

// Subprotocol is the one KubeVirt's console subresources speak.
const Subprotocol = "plain.kubevirt.io"

// Proxy dials the API server as the service account.
type Proxy struct {
	server *url.URL
	tls    *tls.Config
	cfg    *rest.Config
}

// New is a proxy to cfg's API server.
func New(cfg *rest.Config, server string) (*Proxy, error) {
	u, err := url.Parse(server)
	if err != nil {
		return nil, err
	}
	t, err := rest.TLSConfigFor(cfg)
	if err != nil {
		return nil, err
	}
	if t == nil {
		t = &tls.Config{}
	}
	t = t.Clone()
	t.NextProtos = []string{"http/1.1"} // the upgrade is HTTP/1.1 only
	if t.ServerName == "" {
		t.ServerName = u.Hostname()
	}
	return &Proxy{server: u, tls: t, cfg: cfg}, nil
}

// token is read on every dial: the in-cluster token file is rotated.
func (p *Proxy) token() (string, error) {
	if p.cfg.BearerTokenFile != "" {
		b, err := os.ReadFile(p.cfg.BearerTokenFile)
		return strings.TrimSpace(string(b)), err
	}
	return p.cfg.BearerToken, nil
}

// IsUpgrade says whether r asks for a websocket with KubeVirt's subprotocol.
func IsUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Key") == "" {
		return false
	}
	for _, p := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		if strings.TrimSpace(p) == Subprotocol {
			return true
		}
	}
	return false
}

// Serve passes the websocket in r through to path on the API server (e.g.
// /apis/subresources.kubevirt.io/v1/namespaces/ns/virtualmachineinstances/vm/console) until
// either side closes, ctx ends, or until (the session's expiry) passes.
func (p *Proxy) Serve(ctx context.Context, w http.ResponseWriter, r *http.Request, path string, until time.Time) error {
	token, err := p.token()
	if err != nil {
		return err
	}
	host := p.server.Host
	if p.server.Port() == "" {
		host = net.JoinHostPort(p.server.Hostname(), "443")
	}
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: p.tls}
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	upstream, err := dialer.DialContext(dialCtx, "tcp", host)
	if err != nil {
		return fmt.Errorf("dialing the API server: %w", err)
	}

	// The handshake as the browser sent it, minus extensions (no compression to agree on) and
	// cookies, plus the token. Sec-WebSocket-Accept is derived from the browser's key, so virt-api's
	// answer is valid for the browser as it is.
	req := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Scheme: "https", Host: p.server.Host, Path: path},
		Host:   p.server.Host,
		Header: http.Header{
			"Connection":             {"Upgrade"},
			"Upgrade":                {"websocket"},
			"Sec-WebSocket-Version":  {r.Header.Get("Sec-WebSocket-Version")},
			"Sec-WebSocket-Key":      {r.Header.Get("Sec-WebSocket-Key")},
			"Sec-WebSocket-Protocol": {Subprotocol},
			"Authorization":          {"Bearer " + token},
		},
	}
	upstream.SetDeadline(time.Now().Add(15 * time.Second))
	if err := req.Write(upstream); err != nil {
		upstream.Close()
		return fmt.Errorf("sending the handshake: %w", err)
	}
	upReader := bufio.NewReader(upstream)
	resp, err := http.ReadResponse(upReader, req)
	if err != nil {
		upstream.Close()
		return fmt.Errorf("reading the handshake: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		upstream.Close()
		return &UpstreamError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	upstream.SetDeadline(time.Time{})

	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		return fmt.Errorf("the server can't hijack this connection")
	}
	browser, browserRW, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return err
	}
	// From here on the browser's connection is ours: errors are reported as Hijacked, and the
	// caller must not write a response.
	browser.SetDeadline(time.Time{}) // the server's read/write timeouts don't apply to a console
	// Forward virt-api's 101 with only the headers the handshake needs.
	head := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + resp.Header.Get("Sec-WebSocket-Accept") + "\r\n"
	if proto := resp.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
		head += "Sec-WebSocket-Protocol: " + proto + "\r\n"
	}
	if _, err := browser.Write([]byte(head + "\r\n")); err != nil {
		browser.Close()
		upstream.Close()
		return &Hijacked{err}
	}

	closeBoth := func() { browser.Close(); upstream.Close() }
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	expiry := time.AfterFunc(time.Until(until), closeBoth)
	defer expiry.Stop()

	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, browserRW.Reader); closeBoth(); done <- struct{}{} }() // what the browser sent, buffered bytes first
	go func() { io.Copy(browser, upReader); closeBoth(); done <- struct{}{} }()
	<-done
	<-done
	return nil
}

// Hijacked is an error after the connection was taken over: nothing can be written to it any more.
type Hijacked struct{ Err error }

func (e *Hijacked) Error() string { return "after the upgrade: " + e.Err.Error() }
func (e *Hijacked) Unwrap() error { return e.Err }

// UpstreamError is the API server refusing the upgrade.
type UpstreamError struct {
	Status int
	Body   string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("the API server answered %d: %s", e.Status, e.Body)
}
