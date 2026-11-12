// Package machines is the members' VMs: KubeVirt VirtualMachines in one namespace, each labeled with
// its owner. The service is the only writer there; it reads through an informer cache and tells
// listeners about changes as the cache sees them.
package machines

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
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
)

// Error is a refusal with the HTTP status the API answers with.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

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
	write     client.Client // straight to the API server; also the live reads the limit check needs
	http      *http.Client
	hub       *hub
	creates   chan createJob
	log       *slog.Logger
}

// createJob is one Create, run by the create goroutine.
type createJob struct {
	ctx   context.Context
	owner string
	req   Request
	done  chan createResult
}

type createResult struct {
	m   Machine
	err error
}

// createTimeout bounds one create: the usage list, the VM, its Secret and the start.
const createTimeout = time.Minute

// run starts what a Service runs until ctx ends: the event hub and the create goroutine.
func (s *Service) run(ctx context.Context) {
	s.hub = newHub(ctx, s.render)
	s.creates = make(chan createJob)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case j := <-s.creates:
				if err := j.ctx.Err(); err != nil {
					j.done <- createResult{err: err} // the caller gave up while waiting its turn
					continue
				}
				// Once started, a create finishes even if the caller goes away (a half-made machine
				// would count against their limits), but never hangs the queue: one deadline covers it.
				ctx, cancel := context.WithTimeout(context.WithoutCancel(j.ctx), createTimeout)
				m, err := s.create(ctx, j.owner, j.req)
				cancel()
				j.done <- createResult{m, err}
			}
		}
	}()
}

// New connects to the cluster and starts the cache of the namespace's VirtualMachines and
// DataVolumes. It returns once the cache has synced.
func New(ctx context.Context, cfg *rest.Config, namespace string, catalog *config.Catalog, log *slog.Logger) (*Service, error) {
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
	s := &Service{Namespace: namespace, Catalog: catalog, Config: cfg, read: c, write: write, http: httpClient, log: log}
	s.run(ctx)

	vms, err := c.GetInformer(ctx, &kubevirtv1.VirtualMachine{})
	if err != nil {
		return nil, err
	}
	if _, err := vms.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { s.changed(o) },
		UpdateFunc: func(_, o any) { s.changed(o) },
		DeleteFunc: s.changed,
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

// The informers only say which machine changed (owner and id: both are labels only the service
// sets). The hub renders the event from the cache when it sends it.

func (s *Service) changed(o any) {
	if t, ok := o.(toolscache.DeletedFinalStateUnknown); ok {
		o = t.Obj
	}
	if vm, ok := o.(*kubevirtv1.VirtualMachine); ok && vm.Labels[LabelOwner] != "" {
		s.hub.publish(change{owner: vm.Labels[LabelOwner], id: vm.Name})
	}
}

func (s *Service) diskChanged(o any) {
	if dv, ok := o.(*cdiv1.DataVolume); ok && dv.Labels[LabelOwner] != "" && dv.Labels[LabelMachine] != "" {
		s.hub.publish(change{owner: dv.Labels[LabelOwner], id: dv.Labels[LabelMachine]})
	}
}

// render is the event for a change, from the cache as it is now: the machine, or "deleted" when it
// is gone (or, impossibly, belongs to someone else).
func (s *Service) render(c change) Event {
	var vm kubevirtv1.VirtualMachine
	err := s.read.Get(context.Background(), client.ObjectKey{Namespace: s.Namespace, Name: c.id}, &vm)
	if err != nil || vm.Labels[LabelOwner] != c.owner {
		return Event{Kind: "deleted", Data: map[string]string{"id": c.id}}
	}
	return Event{Kind: "vm", Data: toMachine(&vm, s.disk(context.Background(), c.id), s.Catalog)}
}

// Subscribe returns the owner's machine events until cancel is called or the stream falls behind
// (then the channel is closed and the client reconnects for a new snapshot).
func (s *Service) Subscribe(owner string) (<-chan Event, func()) {
	return s.hub.subscribe(owner)
}

func (s *Service) owned(ctx context.Context, owner string) ([]kubevirtv1.VirtualMachine, error) {
	var list kubevirtv1.VirtualMachineList
	if err := s.read.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingLabels{LabelOwner: owner}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// List is the owner's machines, newest first.
func (s *Service) List(ctx context.Context, owner string) ([]Machine, error) {
	vms, err := s.owned(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := make([]Machine, 0, len(vms))
	for i := range vms {
		out = append(out, toMachine(&vms[i], s.disk(ctx, vms[i].Name), s.Catalog))
	}
	slices.SortFunc(out, func(a, b Machine) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

// get is the owner's VM with id. Every request naming a machine goes through here first, and an id
// that isn't one the service could have made is refused before it goes anywhere.
func (s *Service) get(ctx context.Context, owner, id string) (*kubevirtv1.VirtualMachine, error) {
	if !idFormat.MatchString(id) {
		return nil, ErrNotFound
	}
	var vm kubevirtv1.VirtualMachine
	if err := s.read.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: id}, &vm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if vm.Labels[LabelOwner] != owner {
		return nil, ErrNotFound
	}
	return &vm, nil
}

// Get is one of the owner's machines.
func (s *Service) Get(ctx context.Context, owner, id string) (Machine, error) {
	vm, err := s.get(ctx, owner, id)
	if err != nil {
		return Machine{}, err
	}
	return toMachine(vm, s.disk(ctx, id), s.Catalog), nil
}

// Usage is what the owner's machines add up to, running or not.
type Usage struct {
	VMs       int `json:"vms"`
	CPU       int `json:"cpu"`
	MemoryMiB int `json:"memoryMiB"`
	DiskGiB   int `json:"diskGiB"`
}

// Usage adds up the owner's machines, read live from the API server (not the cache, which may not
// have seen a machine created a moment ago).
func (s *Service) Usage(ctx context.Context, owner string) (Usage, error) {
	var list kubevirtv1.VirtualMachineList
	if err := s.write.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingLabels{LabelOwner: owner}); err != nil {
		return Usage{}, err
	}
	vms := list.Items
	var u Usage
	for i := range vms {
		u.VMs++
		u.CPU += annotationInt(&vms[i], AnnotationCPU)
		u.MemoryMiB += annotationInt(&vms[i], AnnotationMem)
		u.DiskGiB += annotationInt(&vms[i], AnnotationDisk)
	}
	return u, nil
}

// Create makes a machine for owner and starts it. Creates run one at a time on the create
// goroutine, each counting usage live, so the limits hold exactly however many arrive at once.
// That needs one running vmservice: run a single replica, and replace it with the Recreate
// strategy so two never overlap.
func (s *Service) Create(ctx context.Context, owner string, r Request) (Machine, error) {
	done := make(chan createResult, 1)
	select {
	case s.creates <- createJob{ctx: ctx, owner: owner, req: r, done: done}:
	case <-ctx.Done():
		return Machine{}, ctx.Err()
	}
	res := <-done
	return res.m, res.err
}

func (s *Service) create(ctx context.Context, owner string, r Request) (Machine, error) {
	u, err := s.Usage(ctx, owner)
	if err != nil {
		return Machine{}, err
	}
	l := s.Catalog.Limits
	switch {
	case u.VMs+1 > l.VMs:
		return Machine{}, refuse(http.StatusForbidden, "You already have %d of %d machines.", u.VMs, l.VMs)
	case u.CPU+r.Size.CPU > l.CPU:
		return Machine{}, refuse(http.StatusForbidden, "This would use %d of your %d vCPUs.", u.CPU+r.Size.CPU, l.CPU)
	case u.MemoryMiB+r.Size.MemoryMiB > l.MemoryMiB:
		return Machine{}, refuse(http.StatusForbidden, "This would use %d of your %d MiB of memory.", u.MemoryMiB+r.Size.MemoryMiB, l.MemoryMiB)
	case u.DiskGiB+r.DiskGiB > l.DiskGiB:
		return Machine{}, refuse(http.StatusForbidden, "This would use %d of your %d GiB of disk.", u.DiskGiB+r.DiskGiB, l.DiskGiB)
	}

	// The VM is created with run strategy Manual (not running), then its cloud-init Secret (owned
	// by it, so it can't outlive it), then it is started. If the service dies in between, the machine shows up stopped or failing
	// to start, and the member can delete it; nothing is left behind unseen.
	var vm *kubevirtv1.VirtualMachine
	for attempt := 0; ; attempt++ {
		vm = buildVM(s.Namespace, newID(), owner, s.Catalog.StorageClass, r)
		err = s.write.Create(ctx, vm)
		if err == nil {
			break
		}
		if !apierrors.IsAlreadyExists(err) || attempt == 3 {
			return Machine{}, s.kubeError("creating the machine", err)
		}
	}
	userData, err := cloudConfig(vm.Name, r)
	if err == nil {
		err = s.write.Create(ctx, buildSecret(vm, userData))
	}
	if err != nil {
		s.write.Delete(context.WithoutCancel(ctx), vm)
		return Machine{}, s.kubeError("creating the machine's cloud-init", err)
	}
	if err := s.subresource(ctx, vm.Name, "start"); err != nil {
		return Machine{}, err
	}
	s.log.Info("machine created", "owner", owner, "id", vm.Name, "image", r.Image.ID, "size", r.Size.ID, "diskGiB", r.DiskGiB)
	return toMachine(vm, nil, s.Catalog), nil
}

// Action is start, stop or restart.
func (s *Service) Action(ctx context.Context, owner, id, action string) error {
	if _, err := s.get(ctx, owner, id); err != nil {
		return err
	}
	return s.subresource(ctx, id, action)
}

// Delete deletes the machine; its disk and cloud-init Secret are garbage-collected with it.
func (s *Service) Delete(ctx context.Context, owner, id string) error {
	vm, err := s.get(ctx, owner, id)
	if err != nil {
		return err
	}
	if err := s.write.Delete(ctx, vm, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
		return s.kubeError("deleting the machine", err)
	}
	s.log.Info("machine deleted", "owner", owner, "id", id)
	return nil
}

// Running says whether the machine has a running instance (its consoles exist).
func (s *Service) Running(ctx context.Context, owner, id string) (bool, error) {
	vm, err := s.get(ctx, owner, id)
	if err != nil {
		return false, err
	}
	return vm.Status.PrintableStatus == kubevirtv1.VirtualMachineStatusRunning || vm.Status.PrintableStatus == kubevirtv1.VirtualMachineStatusPaused, nil
}

// subresource PUTs KubeVirt's start/stop/restart subresource of a VM.
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
