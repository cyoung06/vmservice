package machines

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"golang.org/x/crypto/ssh"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cyoung06/vmservice/internal/store"
)

// Lookup is the read-only view the SSH bastion runs on, in a process of its own: who owns a key,
// whose a machine is (both from the store, which it may only read), and where a running machine is
// (its VirtualMachineInstance, the only Kubernetes object it may read). It can't change anything.
type Lookup struct {
	Namespace string
	// SignInValid is how long after a member's last sign-in their keys are honoured (the session
	// lifetime): removing someone from the group reaches SSH no later than the portal.
	SignInValid time.Duration
	store       store.Store
	read        client.Reader
}

// NewLookup starts a cache of the namespace's VirtualMachineInstances and returns once it synced.
func NewLookup(ctx context.Context, cfg *rest.Config, namespace string, st store.Store, signInValid time.Duration, log *slog.Logger) (*Lookup, error) {
	scheme := runtime.NewScheme()
	if err := kubevirtv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	c, err := cache.New(cfg, cache.Options{Scheme: scheme, DefaultNamespaces: map[string]cache.Config{namespace: {}}})
	if err != nil {
		return nil, err
	}
	if _, err := c.GetInformer(ctx, &kubevirtv1.VirtualMachineInstance{}); err != nil {
		return nil, err
	}
	go func() {
		if err := c.Start(ctx); err != nil {
			log.Error("cache", "err", err)
		}
	}()
	syncCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if !c.WaitForCacheSync(syncCtx) {
		return nil, fmt.Errorf("the cache of %s didn't sync (may the service account list VirtualMachineInstances?)", namespace)
	}
	return &Lookup{Namespace: namespace, SignInValid: signInValid, store: st, read: c}, nil
}

// KeyOwner is the owner of a public key and until when it's honoured: the owner's last sign-in plus
// SignInValid, like a portal session. false if nobody registered it, the stored key isn't exactly
// this one, or the owner hasn't signed in within SignInValid.
func (l *Lookup) KeyOwner(ctx context.Context, pk ssh.PublicKey) (string, time.Time, bool) {
	k, err := l.store.Key(ctx, KeyID(pk))
	if err != nil {
		return "", time.Time{}, false
	}
	stored, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Line))
	if err != nil || string(stored.Marshal()) != string(pk.Marshal()) || k.Owner == "" {
		return "", time.Time{}, false
	}
	at, err := l.store.SignedInAt(ctx, k.Owner)
	until := at.Add(l.SignInValid)
	if err != nil || !time.Now().Before(until) {
		return "", time.Time{}, false
	}
	return k.Owner, until, true
}

// hostName is a display name that can be typed as an SSH host name.
var hostName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// machineFor finds the owner's machine for a host typed after ssh -J: its id, or the display name of
// exactly one of the owner's machines (ignoring case). Only the owner's own machines are searched.
func (l *Lookup) machineFor(ctx context.Context, owner, host string) (store.Machine, error) {
	if idFormat.MatchString(host) {
		row, err := l.store.Machine(ctx, host)
		if errors.Is(err, store.ErrNotFound) || (err == nil && row.Owner != owner) {
			return store.Machine{}, ErrNotFound
		}
		return row, err
	}
	if !hostName.MatchString(host) {
		return store.Machine{}, ErrNotFound
	}
	rows, err := l.store.MachinesNamed(ctx, owner, host)
	switch {
	case err != nil:
		return store.Machine{}, err
	case len(rows) == 0:
		return store.Machine{}, ErrNotFound
	case len(rows) > 1:
		return store.Machine{}, refuse(http.StatusConflict, "%d of your machines are named %q; use the machine id (vm-…)", len(rows), host)
	}
	return rows[0], nil
}

// Address is the pod-network address of the owner's running machine, by id or by name.
func (l *Lookup) Address(ctx context.Context, owner, host string) (string, error) {
	row, err := l.machineFor(ctx, owner, host)
	if err != nil {
		return "", err // refusals are shown to the member; anything else the bastion keeps generic
	}
	id := row.ID
	var vmi kubevirtv1.VirtualMachineInstance
	if err := l.read.Get(ctx, client.ObjectKey{Namespace: l.Namespace, Name: id}, &vmi); err != nil || vmi.Status.Phase != kubevirtv1.Running {
		return "", refuse(http.StatusConflict, "The machine isn't running.")
	}
	for _, iface := range vmi.Status.Interfaces {
		if iface.Name == "default" && iface.IP != "" {
			return iface.IP, nil
		}
	}
	return "", refuse(http.StatusConflict, "The machine has no address yet.")
}
