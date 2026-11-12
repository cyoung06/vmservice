// Package bastion is the SSH jump host: `ssh -J host:port user@vm-xxxxxx`. A member is whoever owns
// the key they connect with (keys are registered in the portal, one member per key); the only thing
// they may do is forward a connection to port 22 of a running machine of theirs. No shell, no other
// hosts or ports, no remote forwarding.
package bastion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

// Machines is what the bastion asks of the machines service.
type Machines interface {
	// KeyOwner is the owner of a registered key and until when it's honoured.
	KeyOwner(ctx context.Context, key ssh.PublicKey) (owner string, until time.Time, ok bool)
	// Address is the address of the owner's running machine id.
	Address(ctx context.Context, owner, id string) (string, error)
}

// Limiter caps each member's open long-lived connections (shared with the consoles).
type Limiter interface {
	Take(owner string) (release func(), ok bool)
}

// Bastion is the SSH server.
type Bastion struct {
	config   *ssh.ServerConfig
	machines Machines
	limit    Limiter
	log      *slog.Logger
	dialPort int           // the port dialed on the machine: 22 (tests point it elsewhere)
	pending  chan struct{} // handshakes in progress: a connection beyond the cap is closed at once
}

const (
	ownerExtension   = "vmservice-owner"
	untilExtension   = "vmservice-until"
	maxPending       = 64 // unauthenticated handshakes at once, for everyone
	maxChannels      = 8  // forwards open at once in one session
	handshakeTimeout = 30 * time.Second
	dialTimeout      = 10 * time.Second
	targetPort       = 22
)

// New reads the host key (an OpenSSH private key file) and builds the server.
func New(hostKeyFile string, m Machines, limit Limiter, log *slog.Logger) (*Bastion, error) {
	pem, err := os.ReadFile(hostKeyFile)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", hostKeyFile, err)
	}
	b := &Bastion{machines: m, limit: limit, log: log, dialPort: targetPort, pending: make(chan struct{}, maxPending)}
	b.config = &ssh.ServerConfig{
		PublicKeyCallback: b.authenticate,
		ServerVersion:     "SSH-2.0-vmservice",
		MaxAuthTries:      6,
	}
	b.config.AddHostKey(signer)
	return b, nil
}

func (b *Bastion) authenticate(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	owner, until, ok := b.machines.KeyOwner(ctx, key)
	if !ok {
		return nil, errors.New("unknown key")
	}
	return &ssh.Permissions{Extensions: map[string]string{ownerExtension: owner, untilExtension: until.Format(time.RFC3339Nano)}}, nil
}

// Serve accepts connections on l until ctx ends. A failed Accept (e.g. out of file descriptors) is
// retried after a backoff, as net/http does, instead of taking the portal down with it.
func (b *Bastion) Serve(ctx context.Context, l net.Listener) error {
	go func() { <-ctx.Done(); l.Close() }()
	var backoff time.Duration
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			backoff = min(max(2*backoff, 5*time.Millisecond), time.Second)
			b.log.Warn("bastion: accept", "err", err, "retry in", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil
			}
			continue
		}
		backoff = 0
		select {
		case b.pending <- struct{}{}:
			go b.handle(ctx, conn)
		default:
			conn.Close() // too many handshakes at once
		}
	}
}

func (b *Bastion) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(handshakeTimeout))
	sc, chans, reqs, err := ssh.NewServerConn(conn, b.config)
	<-b.pending // the handshake is over, one way or the other
	if err != nil {
		return // failed handshakes and unknown keys are routine on a public port
	}
	defer sc.Close()
	conn.SetDeadline(time.Time{})
	owner := sc.Permissions.Extensions[ownerExtension]
	until, _ := time.Parse(time.RFC3339Nano, sc.Permissions.Extensions[untilExtension])
	release, ok := b.limit.Take(owner)
	if !ok {
		b.log.Info("bastion: too many connections", "owner", owner)
		return
	}
	defer release()
	// The session, and every forward in it, ends when the server stops, the client goes, or the
	// owner's sign-in window closes (like a portal session).
	session, cancel := context.WithDeadline(ctx, until)
	defer cancel()
	stop := context.AfterFunc(session, func() { sc.Close() })
	defer stop()
	b.log.Info("bastion: connected", "owner", owner, "remote", conn.RemoteAddr().String())

	go ssh.DiscardRequests(reqs) // global requests (tcpip-forward, keepalives) are all answered no
	open := make(chan struct{}, maxChannels)
	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			nc.Reject(ssh.Prohibited, "this host only forwards to your machines' port 22 (ssh -J)")
			continue
		}
		select {
		case open <- struct{}{}:
			go func() { b.forward(session, owner, nc); <-open }()
		default:
			nc.Reject(ssh.ResourceShortage, "too many forwards in this session")
		}
	}
}

// directTCPIP is RFC 4254 7.2's payload.
type directTCPIP struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

func (b *Bastion) forward(ctx context.Context, owner string, nc ssh.NewChannel) {
	var req directTCPIP
	if err := ssh.Unmarshal(nc.ExtraData(), &req); err != nil {
		nc.Reject(ssh.ConnectionFailed, "bad request")
		return
	}
	if req.Port != targetPort {
		nc.Reject(ssh.Prohibited, "only port 22")
		return
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	addr, err := b.machines.Address(lookup, owner, req.Host)
	cancel()
	if err != nil {
		message := "no such running machine of yours"
		if isRefusal(err) {
			message = err.Error()
		}
		nc.Reject(ssh.ConnectionFailed, req.Host+": "+message)
		return
	}
	d := net.Dialer{Timeout: dialTimeout}
	target, err := d.DialContext(ctx, "tcp", net.JoinHostPort(addr, fmt.Sprint(b.dialPort)))
	if err != nil {
		nc.Reject(ssh.ConnectionFailed, fmt.Sprintf("%s isn't accepting SSH yet", req.Host))
		return
	}
	ch, chReqs, err := nc.Accept()
	if err != nil {
		target.Close()
		return
	}
	go ssh.DiscardRequests(chReqs)
	b.log.Info("bastion: forwarding", "owner", owner, "machine", req.Host)
	// Ends with the session even if the machine keeps its side open.
	stop := context.AfterFunc(ctx, func() { target.Close(); ch.Close() })
	defer stop()
	done := make(chan struct{}, 2)
	go func() { io.Copy(target, ch); closeWrite(target); done <- struct{}{} }()
	go func() { io.Copy(ch, target); ch.CloseWrite(); done <- struct{}{} }()
	<-done
	<-done
	ch.Close()
	target.Close()
}

// isRefusal says whether err is a message meant for the member (the machines service's refusals),
// not an internal error whose text stays in the logs.
func isRefusal(err error) bool {
	var r interface{ HTTPStatus() int }
	return errors.As(err, &r)
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.CloseWrite()
	}
}
