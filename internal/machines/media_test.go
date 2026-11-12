package machines

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cyoung06/vmservice/internal/inspect"
	"github.com/cyoung06/vmservice/internal/store"
)

func isoHeader(t *testing.T) []byte {
	h := make([]byte, inspect.HeaderSize)
	copy(h[16*2048+1:], "CD001")
	copy(h[16*2048+40:], "CCCOMA_X64FRE_EN-US_DV9")
	return h
}

// readyMedium makes alice's medium id look imported and inspected.
func readyMedium(t *testing.T, s *Service, id, kind, profile string) {
	t.Helper()
	ctx := context.Background()
	var dv cdiv1.DataVolume
	if err := s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: id}, &dv); err != nil {
		t.Fatal(err)
	}
	dv.Status.Phase = cdiv1.Succeeded
	if err := s.write.Status().Update(ctx, &dv); err != nil {
		s.write.Update(ctx, &dv)
	}
	s.store.SetMediumProfile(ctx, id, profile, 0)
}

func TestUploadMediumAndWindowsMachine(t *testing.T) {
	s, _ := testService(t)
	s.MediaConfig = MediaConfig{InspectImage: "vmservice:test", DriversImage: "virtio:test"}
	ctx := context.Background()
	if _, err := s.AddUpload(ctx, "alice", "notes", 100, []byte("hello")); status(err) != http.StatusBadRequest {
		t.Errorf("a text file = %v, want 400", err)
	}
	m, err := s.AddUpload(ctx, "alice", "Win 11", 6<<30, isoHeader(t))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != "iso" || m.State != "Uploading" && m.State != "Importing" {
		t.Errorf("medium = %+v", m)
	}
	var dv cdiv1.DataVolume
	if err := s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: m.ID}, &dv); err != nil || dv.Spec.Source.Upload == nil || dv.Labels[LabelOwner] != "alice" {
		t.Fatalf("upload DataVolume = %+v, %v", dv.Spec, err)
	}
	if _, err := s.GetMedium(ctx, "bob", m.ID); status(err) != http.StatusNotFound {
		t.Errorf("bob sees alice's medium: %v", err)
	}
	// Not ready yet: no machine from it.
	r := request(t)
	r.MediaID, r.DiskGiB = m.ID, 64
	if _, err := s.Create(ctx, "alice", r); status(err) != http.StatusConflict {
		t.Errorf("machine from an unready medium = %v, want 409", err)
	}
	readyMedium(t, s, m.ID, "iso", "windows")
	if _, err := s.Create(ctx, "bob", r); status(err) != http.StatusNotFound {
		t.Errorf("bob using alice's medium = %v, want 404", err)
	}
	mach, err := s.Create(ctx, "alice", r)
	if err != nil {
		t.Fatal(err)
	}
	var vm kubevirtv1.VirtualMachine
	s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: mach.ID}, &vm)
	spec := vm.Spec.Template.Spec
	if vm.Spec.DataVolumeTemplates[0].Spec.Source.Blank == nil || len(vm.Spec.DataVolumeTemplates) != 2 ||
		vm.Spec.DataVolumeTemplates[1].Spec.Source.PVC.Name != m.ID {
		t.Errorf("disks: root %+v, templates %d", vm.Spec.DataVolumeTemplates[0].Spec.Source, len(vm.Spec.DataVolumeTemplates))
	}
	names := map[string]bool{}
	for _, v := range spec.Volumes {
		names[v.Name] = true
	}
	if !names[volumeInstall] || !names[volumeDrivers] || names["cloudinit"] {
		t.Errorf("volumes = %v; want install and drivers, no cloud-init", names)
	}
	if spec.Domain.Devices.TPM == nil || spec.Domain.Firmware == nil || *spec.Domain.Firmware.Bootloader.EFI.SecureBoot != true || spec.Domain.Features.SMM == nil {
		t.Error("a Windows machine lacks TPM, Secure Boot or SMM")
	}
	got, _ := s.Get(ctx, "alice", mach.ID)
	if !got.InstallMedia || got.Profile != "windows" || got.Media != m.ID {
		t.Errorf("machine = %+v", got)
	}
	if err := s.Eject(ctx, "alice", mach.ID); err != nil {
		t.Fatal(err)
	}
	s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: mach.ID}, &vm)
	if hasInstallMedia(&vm) || len(vm.Spec.DataVolumeTemplates) != 1 {
		t.Errorf("after eject: volumes %v, templates %d", vm.Spec.Template.Spec.Volumes, len(vm.Spec.DataVolumeTemplates))
	}
	if err := s.Eject(ctx, "alice", mach.ID); status(err) != http.StatusConflict {
		t.Errorf("second eject = %v, want 409", err)
	}
}

func TestDiskImageMachine(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	q := make([]byte, 64)
	copy(q, "QFI\xfb")
	q[24+4] = 0x05 // 20 GiB, big-endian
	m, err := s.AddUpload(ctx, "alice", "custom", 700<<20, q)
	if err != nil {
		t.Fatal(err)
	}
	readyMedium(t, s, m.ID, "disk", "bios")
	r := request(t)
	r.MediaID, r.DiskGiB = m.ID, 10
	if _, err := s.Create(ctx, "alice", r); status(err) != http.StatusBadRequest {
		t.Errorf("a 10 GiB disk for a 20 GiB image = %v, want 400", err)
	}
	r.DiskGiB = 30
	if _, err := s.Create(ctx, "alice", r); status(err) != http.StatusBadRequest {
		t.Errorf("a disk image machine without SSH keys = %v, want 400", err)
	}
	line, _ := newKeyLine(t, "k")
	s.AddKey(ctx, "alice", line, "")
	mach, err := s.Create(ctx, "alice", r)
	if err != nil {
		t.Fatal(err)
	}
	var vm kubevirtv1.VirtualMachine
	s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: mach.ID}, &vm)
	if vm.Spec.DataVolumeTemplates[0].Spec.Source.PVC.Name != m.ID || hasInstallMedia(&vm) {
		t.Errorf("root source = %+v", vm.Spec.DataVolumeTemplates[0].Spec.Source)
	}
	data, _ := s.secretUserData(ctx, mach.ID)
	if len(data) == 0 {
		t.Error("a disk image machine has no cloud-init")
	}
}

// The inspector Job's result becomes the medium's profile; the Job is removed.
func TestInspection(t *testing.T) {
	s, _ := testService(t)
	s.MediaConfig = MediaConfig{InspectImage: "vmservice:test"}
	ctx := context.Background()
	m, err := s.AddUpload(ctx, "alice", "Ubuntu", 2<<30, isoHeader(t))
	if err != nil {
		t.Fatal(err)
	}
	var dv cdiv1.DataVolume
	s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: m.ID}, &dv)
	dv.Status.Phase = cdiv1.Succeeded
	if err := s.write.Status().Update(ctx, &dv); err != nil {
		t.Fatal(err)
	}
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
	row, _ := s.store.Medium(ctx, m.ID)
	if row.Profile != "linux" {
		t.Errorf("profile = %q, want linux", row.Profile)
	}
	if err := s.read.Get(ctx, client.ObjectKey{Namespace: "cloud", Name: job.Name}, &job); err == nil {
		t.Error("the finished Job wasn't removed")
	}
	got, _ := s.GetMedium(ctx, "alice", m.ID)
	if got.State != "Ready" {
		t.Errorf("state = %s, want Ready", got.State)
	}
	_ = time.Now
	_ = store.ErrNotFound
}
