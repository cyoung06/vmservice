package machines

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GehirnInc/crypt/sha512_crypt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	"github.com/cyoung06/vmservice/internal/config"
)

var catalog = &config.Catalog{
	Images: []config.Image{{ID: "ubuntu", Name: "Ubuntu 24.04", Source: "ubuntu-src", DefaultUser: "ubuntu",
		CloudConfig: map[string]any{"apt": map[string]any{"primary": []any{map[string]any{"arches": []any{"default"}, "uri": "http://mirror.kaist.ac.kr/ubuntu/"}}}, "hostname": "evil"}}},
	Sizes:  []config.Size{{ID: "small", Name: "Small", CPU: 2, MemoryMiB: 2048}},
	Disk:   config.Disk{MinGiB: 10, MaxGiB: 100, DefaultGiB: 20},
	Limits: config.Limits{VMs: 2, CPU: 4, MemoryMiB: 8192, DiskGiB: 100},
}

// testService is a Service over a fake client, with KubeVirt's subresources answered by an
// httptest server that records what was called.
func testService(t *testing.T) (*Service, *[]string) {
	t.Helper()
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	kubevirtv1.AddToScheme(scheme)
	cdiv1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	var mu sync.Mutex
	calls := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*calls = append(*calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/stop") {
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"kind":"Status","message":"VM is not running"}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &Service{
		Namespace: "cloud", Catalog: catalog, Config: &rest.Config{Host: srv.URL},
		read: c, write: c, http: srv.Client(), log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	s.run(ctx)
	return s, calls
}

func request(t *testing.T) Request {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	return Request{Name: "my box", Image: catalog.Images[0], Size: catalog.Sizes[0], DiskGiB: 20, PasswordHash: hash}
}

func TestCreateBuildsManualVMThenSecretThenStarts(t *testing.T) {
	s, calls := testService(t)
	ctx := context.Background()
	m, err := s.Create(ctx, "alice", request(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(m.ID, "vm-") || len(m.ID) != 9 || m.Name != "my box" || m.CPU != 2 || m.DiskGiB != 20 || m.DefaultUser != "ubuntu" {
		t.Fatalf("machine = %+v", m)
	}
	var vm kubevirtv1.VirtualMachine
	if err := s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: m.ID}, &vm); err != nil {
		t.Fatal(err)
	}
	if *vm.Spec.RunStrategy != kubevirtv1.RunStrategyManual {
		t.Errorf("run strategy = %s; want Manual: not running until started, and a guest shutdown stays off", *vm.Spec.RunStrategy)
	}
	if vm.Labels[LabelOwner] != "alice" || vm.Spec.Template.ObjectMeta.Labels[LabelOwner] != "alice" {
		t.Errorf("owner labels = %v / %v", vm.Labels, vm.Spec.Template.ObjectMeta.Labels)
	}
	src := vm.Spec.DataVolumeTemplates[0].Spec.Source.PVC
	if src.Namespace != "cloud" || src.Name != "ubuntu-src" {
		t.Errorf("clone source = %+v", src)
	}
	if got := vm.Spec.DataVolumeTemplates[0].Spec.Storage.Resources.Requests.Storage().String(); got != "20Gi" {
		t.Errorf("disk = %s", got)
	}
	var secret corev1.Secret
	if err := s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: m.ID + "-init"}, &secret); err != nil {
		t.Fatal(err)
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Name != m.ID || secret.OwnerReferences[0].Kind != "VirtualMachine" {
		t.Errorf("secret owner = %+v", secret.OwnerReferences)
	}
	if len(*calls) != 1 || *calls != nil && (*calls)[0] != "PUT /apis/subresources.kubevirt.io/v1/namespaces/cloud/virtualmachines/"+m.ID+"/start" {
		t.Errorf("subresource calls = %v", *calls)
	}
}

func TestCloudConfigServiceKeysWin(t *testing.T) {
	r := request(t)
	data, err := cloudConfig("vm-abcdef", r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "#cloud-config\n") {
		t.Fatalf("no #cloud-config header: %q", data)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["hostname"] != "vm-abcdef" || doc["ssh_pwauth"] != false {
		t.Errorf("hostname/ssh_pwauth = %v/%v; the image's extras must not override them", doc["hostname"], doc["ssh_pwauth"])
	}
	if _, ok := doc["apt"]; !ok {
		t.Error("the image's apt extras are missing")
	}
	users := doc["chpasswd"].(map[string]any)["users"].([]any)
	u := users[0].(map[string]any)
	if u["name"] != "ubuntu" || u["type"] != "hash" || !strings.HasPrefix(u["password"].(string), "$6$") {
		t.Errorf("chpasswd user = %v", u)
	}
	if strings.Contains(string(data), "correct horse") {
		t.Error("the plaintext password is in the cloud-config")
	}
	if sha512_crypt.New().Verify(u["password"].(string), []byte("correct horse")) != nil {
		t.Error("the hash doesn't verify the password")
	}
}

func TestOtherMembersMachinesAreNotFound(t *testing.T) {
	s, calls := testService(t)
	ctx := context.Background()
	m, err := s.Create(ctx, "alice", request(t))
	if err != nil {
		t.Fatal(err)
	}
	*calls = nil
	for _, id := range []string{"a/../../secrets", "vm-ABCDEF", "vm-aaaaaa/console", ""} {
		if _, err := s.Get(ctx, "alice", id); err != ErrNotFound {
			t.Errorf("Get(%q) = %v, want not found", id, err)
		}
	}
	if _, err := s.Get(ctx, "bob", m.ID); err != ErrNotFound {
		t.Errorf("bob Get = %v, want not found", err)
	}
	if err := s.Action(ctx, "bob", m.ID, "start"); err != ErrNotFound {
		t.Errorf("bob start = %v, want not found", err)
	}
	if err := s.Delete(ctx, "bob", m.ID); err != ErrNotFound {
		t.Errorf("bob delete = %v, want not found", err)
	}
	if running, err := s.Running(ctx, "bob", m.ID); err != ErrNotFound || running {
		t.Errorf("bob running = %v, %v", running, err)
	}
	if list, _ := s.List(ctx, "bob"); len(list) != 0 {
		t.Errorf("bob sees %v", list)
	}
	if len(*calls) != 0 {
		t.Errorf("bob's requests reached KubeVirt: %v", *calls)
	}
	if err := s.Delete(ctx, "alice", m.ID); err != nil {
		t.Errorf("alice delete = %v", err)
	}
}

func TestLimits(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := s.Create(ctx, "alice", request(t)); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.Create(ctx, "alice", request(t))
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusForbidden {
		t.Fatalf("third machine = %v, want 403 (limit 2 VMs / 4 vCPUs)", err)
	}
	if _, err := s.Create(ctx, "bob", request(t)); err != nil {
		t.Errorf("bob's limits are his own: %v", err)
	}
	u, _ := s.Usage(ctx, "alice")
	if u != (Usage{VMs: 2, CPU: 4, MemoryMiB: 4096, DiskGiB: 40}) {
		t.Errorf("usage = %+v", u)
	}
}

// A member firing many creates at once gets exactly their limit: creates run one at a time and count
// usage live.
func TestParallelCreatesHoldTheLimit(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	made := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Create(ctx, "alice", request(t)); err == nil {
				mu.Lock()
				made++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if made != 2 {
		t.Errorf("20 parallel creates made %d machines; the limit is 2", made)
	}
}

func TestActionRefusalIsPassedOn(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	m, _ := s.Create(ctx, "alice", request(t))
	err := s.Action(ctx, "alice", m.ID, "stop")
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusConflict || !strings.Contains(e.Message, "VM is not running") {
		t.Errorf("stop = %v", err)
	}
}

func TestState(t *testing.T) {
	vm := &kubevirtv1.VirtualMachine{}
	disk := &cdiv1.DataVolume{Status: cdiv1.DataVolumeStatus{Progress: "45.0%"}}
	for printable, want := range map[kubevirtv1.VirtualMachinePrintableStatus]string{
		"": "Provisioning", "Provisioning": "Provisioning", "Stopped": "Stopped", "Starting": "Starting", "Running": "Running",
		"Stopping": "Stopping", "ErrorUnschedulable": "Error", "DataVolumeError": "Error", "CrashLoopBackOff": "Error",
	} {
		vm.Status.PrintableStatus = printable
		if got, _ := state(vm, disk); got != want {
			t.Errorf("%q -> %q, want %q", printable, got, want)
		}
	}
	vm.Status.PrintableStatus = "Provisioning"
	if _, d := state(vm, disk); d != "Copying the disk: 45.0%" {
		t.Errorf("detail = %q", d)
	}
}

func TestEventsGoToTheOwnerOnly(t *testing.T) {
	s, _ := testService(t)
	alice, stopA := s.Subscribe("alice")
	defer stopA()
	bob, stopB := s.Subscribe("bob")
	defer stopB()
	vm := buildVM("cloud", "vm-aaaaaa", "alice", "", request(t))
	if err := s.write.Create(context.Background(), vm); err != nil {
		t.Fatal(err)
	}
	s.changed(vm)
	select {
	case e := <-alice:
		if e.Kind != "vm" || e.Data.(Machine).ID != "vm-aaaaaa" {
			t.Errorf("alice got %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("alice got nothing")
	}
	s.write.Delete(context.Background(), vm)
	s.changed(vm)
	select {
	case e := <-alice:
		if e.Kind != "deleted" {
			t.Errorf("alice got %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("alice got no delete")
	}
	select {
	case e := <-bob:
		t.Errorf("bob got alice's event %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}
