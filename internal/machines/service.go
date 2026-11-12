// Package machines is the members' VMs. A machine is a KubeVirt VirtualMachine in one namespace
// (what it is and does: KubeVirt's spec and status) plus a row in the store (whose it is: the
// rights). The service is the only writer of both; it reads VMs through an informer cache and tells
// listeners about changes as the cache sees them.
//
// Create inserts the row first (its limit check is one SERIALIZABLE transaction), then the VM. A
// crash in between leaves a row without a VM, which the member sees as failed and can delete; never
// a VM nobody owns. Delete deletes the VM; the VM watch removes the row once the VM is gone.
package machines

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cyoung06/vmservice/internal/config"
	"github.com/cyoung06/vmservice/internal/store"
)

// Error is a refusal with the HTTP status the API answers with.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// HTTPStatus marks the error as a refusal meant for the member (its message is safe to show).
func (e *Error) HTTPStatus() int { return e.Status }

func refuse(status int, format string, args ...any) *Error {
	return &Error{status, fmt.Sprintf(format, args...)}
}

// ErrNotFound is a machine that doesn't exist or isn't the caller's: the two look the same.
var ErrNotFound = refuse(http.StatusNotFound, "No such machine.")

// Service is the members' machines in Namespace.
type Service struct {
	Namespace string
	Catalog   *config.Catalog
	Config    *rest.Config  // the service account's, for the subresources and the consoles
	read      client.Reader // the informer cache
	write     client.Client // straight to the API server
	store     store.Store
	http      *http.Client
	hub       *hub
	log       *slog.Logger
}

// New connects to the cluster and starts the cache of the namespace's VirtualMachines and
// DataVolumes. It returns once the cache has synced.
func New(ctx context.Context, cfg *rest.Config, namespace string, catalog *config.Catalog, st store.Store, log *slog.Logger) (*Service, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, kubevirtv1.AddToScheme, cdiv1.AddToScheme} {
		if err := add(scheme); err != nil {
			return nil, err
		}
	}
	c, err := cache.New(cfg, cache.Options{Scheme: scheme, DefaultNamespaces: map[string]cache.Config{namespace: {}}})
	if err != nil {
		return nil, err
	}
	write, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, err
	}
	s := &Service{Namespace: namespace, Catalog: catalog, Config: cfg, read: c, write: write, store: st, http: httpClient, log: log}
	s.hub = newHub(ctx, s.render)

	vms, err := c.GetInformer(ctx, &kubevirtv1.VirtualMachine{})
	if err != nil {
		return nil, err
	}
	if _, err := vms.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { s.changed(o) },
		UpdateFunc: func(_, o any) { s.changed(o) },
		DeleteFunc: s.vmGone,
	}); err != nil {
		return nil, err
	}
	disks, err := c.GetInformer(ctx, &cdiv1.DataVolume{})
	if err != nil {
		return nil, err
	}
	// A disk's clone progress is part of its machine's state.
	if _, err := disks.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { s.diskChanged(o) },
		UpdateFunc: func(_, o any) { s.diskChanged(o) },
	}); err != nil {
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
		return nil, fmt.Errorf("the cache of %s didn't sync (are KubeVirt and CDI installed, and may the service account list them?)", namespace)
	}
	return s, nil
}

func (s *Service) disk(ctx context.Context, id string) *cdiv1.DataVolume {
	var dv cdiv1.DataVolume
	if s.read.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: diskName(id)}, &dv) != nil {
		return nil
	}
	return &dv
}

func (s *Service) vm(ctx context.Context, id string) *kubevirtv1.VirtualMachine {
	var vm kubevirtv1.VirtualMachine
	if s.read.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: id}, &vm) != nil {
		return nil
	}
	return &vm
}

// The informers only say which machine changed; the hub renders the event when it sends it. The
// owner label routes the event (only the service sets it); render checks it against the row.

func vmOf(o any) *kubevirtv1.VirtualMachine {
	if t, ok := o.(toolscache.DeletedFinalStateUnknown); ok {
		o = t.Obj
	}
	vm, _ := o.(*kubevirtv1.VirtualMachine)
	return vm
}

func (s *Service) changed(o any) {
	if vm := vmOf(o); vm != nil && vm.Labels[LabelOwner] != "" {
		s.hub.publish(change{owner: vm.Labels[LabelOwner], id: vm.Name})
	}
}

// vmGone removes a deleted VM's row: the row's only remover once a VM existed.
func (s *Service) vmGone(o any) {
	vm := vmOf(o)
	if vm == nil || !idFormat.MatchString(vm.Name) {
		return
	}
	go func() { // off the informer's goroutine: a slow database mustn't hold up everyone's live state
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.store.DeleteMachine(ctx, vm.Name); err != nil {
			s.log.Error("removing a deleted machine's row", "id", vm.Name, "err", err)
		}
		s.changed(vm)
	}()
}

func (s *Service) diskChanged(o any) {
	if dv, ok := o.(*cdiv1.DataVolume); ok && dv.Labels[LabelOwner] != "" && dv.Labels[LabelMachine] != "" {
		s.hub.publish(change{owner: dv.Labels[LabelOwner], id: dv.Labels[LabelMachine]})
	}
}

// render is the event for a change, from the store and the cache as they are now: the machine, or
// "deleted" when its row is gone or isn't this owner's.
func (s *Service) render(c change) Event {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row, err := s.store.Machine(ctx, c.id)
	if err != nil || row.Owner != c.owner {
		return Event{Kind: "deleted", Data: map[string]string{"id": c.id}}
	}
	return Event{Kind: "vm", Data: toMachine(row, s.vm(ctx, c.id), s.disk(ctx, c.id), s.Catalog, time.Now())}
}

// Subscribe returns the owner's machine events until cancel is called or the stream falls behind
// (then the channel is closed and the client reconnects for a new snapshot).
func (s *Service) Subscribe(owner string) (<-chan Event, func()) {
	return s.hub.subscribe(owner)
}

// List is the owner's machines, newest first.
func (s *Service) List(ctx context.Context, owner string) ([]Machine, error) {
	rows, err := s.store.Machines(ctx, owner)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]Machine, 0, len(rows))
	for _, row := range rows {
		out = append(out, toMachine(row, s.vm(ctx, row.ID), s.disk(ctx, row.ID), s.Catalog, now))
	}
	return out, nil
}

// get is the owner's machine id: its row and, if it exists, its VM. Every request naming a machine
// goes through here first; an id the service couldn't have made is refused before it goes anywhere.
func (s *Service) get(ctx context.Context, owner, id string) (store.Machine, *kubevirtv1.VirtualMachine, error) {
	if !idFormat.MatchString(id) {
		return store.Machine{}, nil, ErrNotFound
	}
	row, err := s.store.Machine(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && row.Owner != owner) {
		return store.Machine{}, nil, ErrNotFound
	}
	if err != nil {
		return store.Machine{}, nil, s.storeError("reading the machine", err)
	}
	return row, s.vm(ctx, id), nil
}

// Get is one of the owner's machines.
func (s *Service) Get(ctx context.Context, owner, id string) (Machine, error) {
	row, vm, err := s.get(ctx, owner, id)
	if err != nil {
		return Machine{}, err
	}
	return toMachine(row, vm, s.disk(ctx, id), s.Catalog, time.Now()), nil
}

// EnsureMember adds the member if a session from before they had a row is in use.
func (s *Service) EnsureMember(ctx context.Context, owner, username string) error {
	if err := s.store.EnsureMember(ctx, owner, username); err != nil {
		return s.storeError("recording you", err)
	}
	return nil
}

// Usage adds up the owner's machines, running or not.
func (s *Service) Usage(ctx context.Context, owner string) (store.Usage, error) {
	return s.store.Usage(ctx, owner)
}

// createTimeout bounds the steps after the row: the VM, its Secret and the start.
const createTimeout = time.Minute

// Create makes a machine for owner and starts it.
func (s *Service) Create(ctx context.Context, owner string, r Request) (Machine, error) {
	l := s.Catalog.Limits
	check := func(u store.Usage) error {
		switch {
		case u.VMs+1 > l.VMs:
			return &store.LimitError{Message: fmt.Sprintf("You already have %d of %d machines.", u.VMs, l.VMs)}
		case u.CPU+r.Size.CPU > l.CPU:
			return &store.LimitError{Message: fmt.Sprintf("This would use %d of your %d vCPUs.", u.CPU+r.Size.CPU, l.CPU)}
		case u.MemoryMiB+r.Size.MemoryMiB > l.MemoryMiB:
			return &store.LimitError{Message: fmt.Sprintf("This would use %d of your %d MiB of memory.", u.MemoryMiB+r.Size.MemoryMiB, l.MemoryMiB)}
		case u.DiskGiB+r.DiskGiB > l.DiskGiB:
			return &store.LimitError{Message: fmt.Sprintf("This would use %d of your %d GiB of disk.", u.DiskGiB+r.DiskGiB, l.DiskGiB)}
		}
		return nil
	}
	// The member's SSH keys as they are now (later ones are managed inside the machine). Without
	// keys or a password nothing could sign in to the machine.
	keys, err := s.store.Keys(ctx, owner)
	if err != nil {
		return Machine{}, s.storeError("reading your SSH keys", err)
	}
	if len(keys) == 0 && r.PasswordHash == "" {
		return Machine{}, refuse(http.StatusBadRequest, "Set a password or add an SSH key first: otherwise nothing can sign in to the machine.")
	}
	for _, k := range keys {
		r.AuthorizedKeys = append(r.AuthorizedKeys, k.Line)
	}
	var row store.Machine
	for attempt := 0; ; attempt++ {
		row = store.Machine{ID: newID(), Owner: owner, Name: r.Name, Image: r.Image.ID, Size: r.Size.ID, CPU: r.Size.CPU, MemoryMiB: r.Size.MemoryMiB, DiskGiB: r.DiskGiB}
		err := s.store.CreateMachine(ctx, row, check)
		var le *store.LimitError
		switch {
		case err == nil:
		case errors.As(err, &le):
			return Machine{}, refuse(http.StatusForbidden, "%s", le.Message)
		case errors.Is(err, store.ErrExists) && attempt < 3:
			continue
		default:
			return Machine{}, s.storeError("saving the machine", err)
		}
		break
	}
	row.CreatedAt = time.Now()

	// From here the machine is the member's; the rest finishes even if they go away (one deadline
	// covers it). The VM is created with run strategy Manual (not running), then its cloud-init
	// Secret (owned by it, so it can't outlive it), then it is started.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), createTimeout)
	defer cancel()
	// A failed VM create gives the row (and the limit) back, but only once the API server says the
	// VM really doesn't exist: a create that timed out may still have happened, and its row must stay.
	abandon := func(what string, err error) (Machine, error) {
		var live kubevirtv1.VirtualMachine
		if gerr := s.write.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: row.ID}, &live); apierrors.IsNotFound(gerr) {
			s.store.DeleteMachine(ctx, row.ID)
		}
		return Machine{}, s.kubeError(what, err)
	}
	vm := buildVM(s.Namespace, row.ID, owner, s.Catalog.StorageClass, r)
	if err := s.write.Create(ctx, vm); err != nil {
		return abandon("creating the machine", err)
	}
	userData, err := cloudConfig(vm.Name, r)
	if err == nil {
		err = s.write.Create(ctx, buildSecret(vm, userData))
	}
	if err != nil {
		s.write.Delete(ctx, vm) // the watch then removes the row
		return Machine{}, s.kubeError("creating the machine's cloud-init", err)
	}
	if err := s.subresource(ctx, vm.Name, "start"); err != nil {
		return Machine{}, err
	}
	s.log.Info("machine created", "owner", owner, "id", vm.Name, "image", r.Image.ID, "size", r.Size.ID, "diskGiB", r.DiskGiB)
	return toMachine(row, vm, nil, s.Catalog, time.Now()), nil
}

// Action is start, stop or restart.
func (s *Service) Action(ctx context.Context, owner, id, action string) error {
	_, vm, err := s.get(ctx, owner, id)
	if err != nil {
		return err
	}
	if vm == nil {
		return refuse(http.StatusConflict, "The machine's VM is missing; delete the machine.")
	}
	return s.subresource(ctx, id, action)
}

// Delete deletes the machine's VM (its disk and cloud-init Secret go with it); the watch removes the
// row once the VM is gone. A machine whose VM is already missing loses its row here.
func (s *Service) Delete(ctx context.Context, owner, id string) error {
	row, vm, err := s.get(ctx, owner, id)
	if err != nil {
		return err
	}
	// A machine whose VM the cache doesn't have loses its row only when the API server confirms the
	// VM is missing and the row is older than a create can take: a create still under way would
	// otherwise go on to make a VM nobody owns.
	if vm == nil {
		var live kubevirtv1.VirtualMachine
		err := s.write.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: id}, &live)
		switch {
		case err == nil:
			vm = &live
		case !apierrors.IsNotFound(err):
			return s.kubeError("reading the machine", err)
		case time.Since(row.CreatedAt) < missingGrace:
			return refuse(http.StatusConflict, "The machine is still being created; try again in a moment.")
		default:
			if err := s.store.DeleteMachine(ctx, id); err != nil {
				return s.storeError("deleting the machine", err)
			}
			s.hub.publish(change{owner: owner, id: id})
		}
	}
	if vm != nil {
		if err := s.write.Delete(ctx, vm, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
			return s.kubeError("deleting the machine", err)
		}
	}
	s.log.Info("machine deleted", "owner", owner, "id", id)
	return nil
}

// Running says whether the machine has a running instance (its consoles exist).
func (s *Service) Running(ctx context.Context, owner, id string) (bool, error) {
	_, vm, err := s.get(ctx, owner, id)
	if err != nil || vm == nil {
		return false, err
	}
	return vm.Status.PrintableStatus == kubevirtv1.VirtualMachineStatusRunning || vm.Status.PrintableStatus == kubevirtv1.VirtualMachineStatusPaused, nil
}

func (s *Service) storeError(what string, err error) error {
	s.log.Warn("store", "what", what, "err", err)
	return refuse(http.StatusBadGateway, "The database refused %s.", what)
}

func (s *Service) subresource(ctx context.Context, id, action string) error {
	url := APIServer(s.Config) + fmt.Sprintf("/apis/subresources.kubevirt.io/v1/namespaces/%s/virtualmachines/%s/%s", s.Namespace, id, action)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return s.kubeError(action, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var status metav1.Status
	message := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &status) == nil && status.Message != "" {
		message = status.Message
	}
	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusBadRequest {
		// KubeVirt refuses e.g. stopping a stopped VM; say what it said.
		return refuse(http.StatusConflict, "KubeVirt refused to %s the machine: %s", action, message)
	}
	s.log.Warn("subresource", "action", action, "id", id, "status", resp.StatusCode, "message", message)
	return refuse(http.StatusBadGateway, "The cluster couldn't %s the machine.", action)
}

func (s *Service) kubeError(what string, err error) error {
	s.log.Warn("kubernetes", "what", what, "err", err)
	return refuse(http.StatusBadGateway, "The cluster refused %s.", what)
}

// APIServer is the API server's base URL, with a scheme.
func APIServer(cfg *rest.Config) string {
	host := strings.TrimRight(cfg.Host, "/")
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	return host
}
