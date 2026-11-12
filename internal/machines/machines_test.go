package machines

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	"github.com/cyoung06/vmservice/internal/config"
	"github.com/cyoung06/vmservice/internal/inspect"
	"github.com/cyoung06/vmservice/internal/store"
)

var catalog = &config.Catalog{
	Sizes:  []config.Size{{ID: "small", Name: "Small", CPU: 2, MemoryMiB: 2048}},
	Disk:   config.Disk{MinGiB: 10, MaxGiB: 100, DefaultGiB: 20},
	Limits: config.Limits{VMs: 2, CPU: 4, MemoryMiB: 8192, DiskGiB: 100},
}

var (
	alice = Actor{Owner: "alice", Username: "alice"}
	bob   = Actor{Owner: "bob", Username: "bob"}
	admin = Actor{Owner: "root", Username: "root", Admin: true}
)

// testService is a Service over a fake client and an in-memory store, with KubeVirt's subresources
// answered by an httptest server that records what was called.
func testService(t *testing.T) (*Service, *[]string) {
	t.Helper()
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	batchv1.AddToScheme(scheme)
	kubevirtv1.AddToScheme(scheme)
	cdiv1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&cdiv1.DataVolume{}).Build()
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
	st := store.NewMemory()
	for _, a := range []Actor{alice, bob, admin} {
		st.UpsertMember(ctx, a.Owner, a.Username)
	}
	s := &Service{
		Namespace: "cloud", Catalog: catalog, Config: &rest.Config{Host: srv.URL},
		read: c, write: c, watch: c, store: st, http: srv.Client(), log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		MediaConfig: MediaConfig{InspectImage: "vmservice:test", DriversImage: "virtio:test"},
	}
	s.hub = newHub(ctx, s.render)
	return s, calls
}

func status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func isoHeader() []byte {
	h := make([]byte, inspect.HeaderSize)
	copy(h[16*2048+1:], "CD001")
	copy(h[16*2048+40:], "CCCOMA_X64FRE_EN-US_DV9")
	return h
}

func qcowHeader(virtualGiB uint64) []byte {
	q := make([]byte, 64)
	copy(q, "QFI\xfb")
	v := virtualGiB << 30
	for i := 0; i < 8; i++ {
		q[24+i] = byte(v >> (56 - 8*i))
	}
	return q
}

func markImported(t *testing.T, s *Service, id string) {
	t.Helper()
	ctx := context.Background()
	var dv cdiv1.DataVolume
	if err := s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: id}, &dv); err != nil {
		t.Fatal(err)
	}
	dv.Status.Phase = cdiv1.Succeeded
	if err := s.write.Status().Update(ctx, &dv); err != nil {
		t.Fatal(err)
	}
}

// readyImage adds an image for a and makes it look imported and inspected.
func readyImage(t *testing.T, s *Service, a Actor, header []byte, size int64, profile string) string {
	t.Helper()
	m, err := s.AddUpload(context.Background(), a, "img", size, header)
	if err != nil {
		t.Fatal(err)
	}
	markImported(t, s, m.ID)
	s.store.SetMediumProfile(context.Background(), m.ID, profile, 0)
	return m.ID
}

func request(name, image string) Request {
	hash, _ := HashPassword("correct horse")
	return Request{Name: name, MediaID: image, Size: catalog.Sizes[0], DiskGiB: 30, PasswordHash: hash}
}

func (s *Service) secretUserData(ctx context.Context, id string) (map[string]any, error) {
	var secret corev1.Secret
	if err := s.read.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: id + "-init"}, &secret); err != nil {
		return nil, err
	}
	var doc map[string]any
	err := yaml.Unmarshal(secret.Data["userdata"], &doc)
	return doc, err
}

func TestCreateFromPresetGivesTheMembersAccount(t *testing.T) {
	s, calls := testService(t)
	ctx := context.Background()
	img := readyImage(t, s, bob, qcowHeader(5), 700<<20, "bios")
	yes := true
	extras := map[string]any{"apt": map[string]any{"primary": []any{"x"}}}
	if _, err := s.UpdateMedium(ctx, bob, img, store.MediumUpdate{Public: &yes, CloudConfig: extras}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateMedium(ctx, bob, img, store.MediumUpdate{CloudConfig: map[string]any{"users": []any{"evil"}}}); status(err) != http.StatusBadRequest {
		t.Errorf("extras setting users = %v, want 400", err)
	}
	uid := 10001
	s.SetUID(ctx, alice, &uid)
	m, err := s.Create(ctx, alice, request("box1", img))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "box1" || m.User != "alice" || m.Media != img || m.Owner != "alice" {
		t.Errorf("machine = %+v", m)
	}
	var vm kubevirtv1.VirtualMachine
	s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: m.ID}, &vm)
	if src := vm.Spec.DataVolumeTemplates[0].Spec.Source.PVC; src == nil || src.Name != img {
		t.Errorf("root source = %+v", vm.Spec.DataVolumeTemplates[0].Spec.Source)
	}
	doc, err := s.secretUserData(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if doc["hostname"] != "box1" || doc["apt"] == nil || doc["ssh_pwauth"] != false {
		t.Errorf("cloud-config = %v", doc)
	}
	users := doc["users"].([]any)
	acct := users[1].(map[string]any)
	if users[0] != "default" || acct["name"] != "alice" || acct["uid"] != float64(10001) || !strings.HasPrefix(acct["passwd"].(string), "$6$") || acct["lock_passwd"] != false {
		t.Errorf("users = %v", users)
	}
	if len(*calls) != 1 || !strings.HasSuffix((*calls)[0], "/"+m.ID+"/start") {
		t.Errorf("subresource calls = %v", *calls)
	}
}

func TestNames(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	img := readyImage(t, s, alice, qcowHeader(5), 700<<20, "bios")
	for _, name := range []string{"Box", "-x", "has space", "vm-abcdef", strings.Repeat("a", 64)} {
		if _, err := s.Create(ctx, alice, request(name, img)); status(err) != http.StatusBadRequest {
			t.Errorf("name %q = %v, want 400", name, err)
		}
	}
	if _, err := s.Create(ctx, alice, request("taken", img)); err != nil {
		t.Fatal(err)
	}
	yes := true
	s.UpdateMedium(ctx, alice, img, store.MediumUpdate{Public: &yes})
	if _, err := s.Create(ctx, bob, request("taken", img)); status(err) != http.StatusConflict {
		t.Errorf("another member reusing a name = %v, want 409 (names are unique across everyone)", err)
	}
	if ok, _, _ := s.NameAvailable(ctx, "taken"); ok {
		t.Error("a taken name is available")
	}
	if ok, _, _ := s.NameAvailable(ctx, "free-one"); !ok {
		t.Error("a free name isn't available")
	}
}

func TestImageVisibilityAndAdmin(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	img := readyImage(t, s, alice, qcowHeader(5), 700<<20, "bios")
	if _, err := s.Create(ctx, bob, request("bobs", img)); status(err) != http.StatusBadRequest {
		t.Errorf("bob using alice's private image = %v, want 400", err)
	}
	if list, _ := s.Media(ctx, bob); len(list) != 0 {
		t.Errorf("bob sees %v", list)
	}
	yes := true
	if _, err := s.UpdateMedium(ctx, bob, img, store.MediumUpdate{Public: &yes}); status(err) != http.StatusNotFound {
		t.Errorf("bob publishing alice's image = %v, want 404", err)
	}
	s.UpdateMedium(ctx, alice, img, store.MediumUpdate{Public: &yes})
	if list, _ := s.Media(ctx, bob); len(list) != 1 || list[0].Mine || list[0].Owner != "alice" {
		t.Errorf("bob's list = %+v", list)
	}
	if err := s.DeleteMedium(ctx, bob, img); status(err) != http.StatusNotFound {
		t.Errorf("bob deleting alice's public image = %v, want 404", err)
	}
	m, err := s.Create(ctx, alice, request("alices", img))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, bob, m.ID); err != ErrNotFound {
		t.Errorf("bob Get = %v", err)
	}
	if got, err := s.Get(ctx, admin, m.ID); err != nil || got.Owner != "alice" {
		t.Errorf("admin Get = %+v, %v", got, err)
	}
	if _, err := s.AllMachines(ctx, bob); status(err) != http.StatusForbidden {
		t.Errorf("bob AllMachines = %v, want 403", err)
	}
	if all, _ := s.AllMachines(ctx, admin); len(all) != 1 {
		t.Errorf("admin AllMachines = %v", all)
	}
	if err := s.DeleteMedium(ctx, admin, img); err != nil {
		t.Errorf("admin deleting an image = %v", err)
	}
	if err := s.Delete(ctx, admin, m.ID); err != nil {
		t.Errorf("admin deleting a machine = %v", err)
	}
}

func TestQuotas(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	img := readyImage(t, s, alice, qcowHeader(5), 700<<20, "bios")
	var wg sync.WaitGroup
	var mu sync.Mutex
	made := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := request("m"+string(rune('a'+i)), img)
			r.Size.CPU = 1
			if _, err := s.Create(ctx, alice, r); err == nil {
				mu.Lock()
				made++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if made != 2 {
		t.Errorf("10 parallel creates made %d; the default limit is 2", made)
	}
	three := 3
	if err := s.SetQuota(ctx, bob, "alice", store.Quota{VMs: &three}); status(err) != http.StatusForbidden {
		t.Errorf("bob setting quotas = %v, want 403", err)
	}
	if err := s.SetQuota(ctx, admin, "alice", store.Quota{VMs: &three}); err != nil {
		t.Fatal(err)
	}
	r := request("third", img)
	r.Size.CPU = 1
	if _, err := s.Create(ctx, alice, r); err != nil {
		t.Errorf("third machine with a quota of 3 = %v", err)
	}
	yes := true
	s.UpdateMedium(ctx, alice, img, store.MediumUpdate{Public: &yes})
	for i := 0; i < 4; i++ {
		if _, err := s.Create(ctx, admin, request("adm"+string(rune('a'+i)), img)); err != nil {
			t.Errorf("admins have no limits: %v", err)
		}
	}
	members, _ := s.Members(ctx, admin)
	for _, m := range members {
		if m.Username == "alice" && (m.Usage.VMs != 3 || *m.Quota.VMs != 3) {
			t.Errorf("alice = %+v", m)
		}
	}
}

func TestPasswordAndWindows(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	img := readyImage(t, s, alice, qcowHeader(5), 700<<20, "bios")
	r := request("nopass", img)
	r.PasswordHash = ""
	if _, err := s.Create(ctx, alice, r); status(err) != http.StatusBadRequest {
		t.Errorf("no password and no key = %v, want 400", err)
	}
	win := readyImage(t, s, alice, isoHeader(), 6<<30, "windows")
	r = request("win", win)
	r.DiskGiB = 64
	m, err := s.Create(ctx, alice, r)
	if err != nil {
		t.Fatal(err)
	}
	if m.User != "" || m.Profile != "windows" || !m.InstallMedia {
		t.Errorf("windows machine = %+v", m)
	}
	if _, err := s.secretUserData(ctx, m.ID); err == nil {
		t.Error("a Windows machine got cloud-init")
	}
	var vm kubevirtv1.VirtualMachine
	s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: m.ID}, &vm)
	spec := vm.Spec.Template.Spec
	if spec.Domain.Devices.TPM == nil || !*spec.Domain.Firmware.Bootloader.EFI.SecureBoot || spec.Domain.Features.SMM == nil || !hasVolume(&vm, volumeDrivers) {
		t.Error("a Windows machine lacks TPM, Secure Boot, SMM or the driver CD")
	}
	if err := s.Eject(ctx, alice, m.ID); err != nil {
		t.Fatal(err)
	}
	s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: m.ID}, &vm)
	if hasInstallMedia(&vm) || len(vm.Spec.DataVolumeTemplates) != 1 {
		t.Error("eject left the CD-ROMs")
	}
	if err := s.Eject(ctx, alice, m.ID); status(err) != http.StatusConflict {
		t.Errorf("second eject = %v, want 409", err)
	}
}

// The inspector Job's result becomes the image's profile; the Job is removed.
func TestInspection(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	m, err := s.AddUpload(ctx, alice, "Ubuntu", 2<<30, isoHeader())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUpload(ctx, alice, "notes", 100, []byte("hello")); status(err) != http.StatusBadRequest {
		t.Errorf("a text file = %v, want 400", err)
	}
	markImported(t, s, m.ID)
	var dv cdiv1.DataVolume
	s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: m.ID}, &dv)
	s.ensureInspected(&dv)
	var job batchv1.Job
	if err := s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: "inspect-" + m.ID}, &job); err != nil {
		t.Fatalf("no inspector Job: %v", err)
	}
	spec := job.Spec.Template.Spec
	if *spec.AutomountServiceAccountToken || !spec.Containers[0].VolumeMounts[0].ReadOnly || job.Spec.Template.Labels["app"] != "vmservice-inspect" {
		t.Errorf("inspector Job isn't locked down: %+v", spec)
	}
	out, _ := json.Marshal(inspect.Result{Kind: "iso", Profile: "linux", VirtualBytes: 2 << 30})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-x", Namespace: "cloud", Labels: map[string]string{"job-name": job.Name}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: string(out)}}}}}}
	if err := s.write.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	job.Status.Succeeded = 1
	s.inspected(&job)
	if got, _ := s.GetMedium(ctx, alice, m.ID); got.State != "Ready" || got.Profile != "linux" {
		t.Errorf("image = %+v, want Ready/linux", got)
	}
	if err := s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: job.Name}, &job); err == nil {
		t.Error("the finished Job wasn't removed")
	}
}

func TestEvents(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	alices, stopA := s.Subscribe("alice")
	defer stopA()
	bobs, stopB := s.Subscribe("bob")
	defer stopB()
	admins, stopAdm := s.Subscribe(Everyone)
	defer stopAdm()
	img := readyImage(t, s, alice, qcowHeader(5), 700<<20, "bios")
	expect := func(name string, ch <-chan Event, kind string) {
		t.Helper()
		for {
			select {
			case e := <-ch:
				if e.Kind == kind {
					return
				}
			case <-time.After(time.Second):
				t.Fatalf("%s got no %s event", name, kind)
			}
		}
	}
	yes, no := true, false
	s.UpdateMedium(ctx, alice, img, store.MediumUpdate{Public: &yes})
	expect("bob", bobs, "image") // a public image reaches everyone
	s.UpdateMedium(ctx, alice, img, store.MediumUpdate{Public: &no})
	expect("bob", bobs, "image-deleted") // and unpublishing takes it away again
	m, err := s.Create(ctx, alice, request("evt", img))
	if err != nil {
		t.Fatal(err)
	}
	var vm kubevirtv1.VirtualMachine
	s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: m.ID}, &vm)
	for len(bobs) > 0 {
		<-bobs
	}
	s.changed(&vm)
	expect("alice", alices, "vm")
	expect("admin", admins, "vm")
	select {
	case e := <-bobs:
		t.Errorf("bob got %+v", e)
	case <-time.After(50 * time.Millisecond):
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
}

func TestDeleteMissingVM(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	s.store.CreateMachine(ctx, store.Machine{ID: "vm-mmmmmm", Owner: "alice", Name: "ghost"}, func(store.Usage, store.Quota) error { return nil })
	if err := s.Delete(ctx, alice, "vm-mmmmmm"); status(err) != http.StatusConflict {
		t.Errorf("delete right after the row = %v, want 409 (a create may still be making the VM)", err)
	}
}

func TestLookupByName(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	s.store.CreateMachine(ctx, store.Machine{ID: "vm-aaaaaa", Owner: "alice", Name: "mybox"}, func(store.Usage, store.Quota) error { return nil })
	l := &Lookup{Namespace: s.Namespace, store: s.store, read: s.read}
	for host, want := range map[string]int{"mybox": http.StatusConflict, "MYBOX": http.StatusConflict, "vm-aaaaaa": http.StatusConflict, "nothere": 404, "../x": 404} {
		_, err := l.Address(ctx, host)
		got := status(err)
		if err == ErrNotFound {
			got = 404
		}
		if got != want {
			t.Errorf("Address(%q) = %v, want %d (409: found but not running)", host, err, want)
		}
	}
}
