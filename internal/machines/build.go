package machines

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/GehirnInc/crypt/sha512_crypt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/yaml"

	"github.com/cyoung06/vmservice/internal/config"
)

// The labels and annotations the service puts on what it creates. Only the service writes to the
// namespace, so they are trusted.
const (
	LabelOwner     = "vmservice/owner"      // the member's stable id (auth.OwnerID), not their username
	LabelMachine   = "vmservice/machine"    // the machine id, also on the VM's pod
	AnnotationName = "vmservice/name"       // the display name
	AnnotationUser = "vmservice/owner-name" // the owner's username when the machine was made, for admins
	AnnotationImg  = "vmservice/image"
	AnnotationSize = "vmservice/size"
	AnnotationCPU  = "vmservice/cpu"
	AnnotationMem  = "vmservice/memory-mib"
	AnnotationDisk = "vmservice/disk-gib"
)

// Machine is a member's VM as the API shows it.
type Machine struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Image       string    `json:"image"`
	ImageName   string    `json:"imageName"`
	DefaultUser string    `json:"defaultUser"`
	Size        string    `json:"size"`
	CPU         int       `json:"cpu"`
	MemoryMiB   int       `json:"memoryMiB"`
	DiskGiB     int       `json:"diskGiB"`
	State       string    `json:"state"`
	Detail      string    `json:"detail"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Request is what a member asks for in POST /api/vms, after checking.
type Request struct {
	Name string
	// OwnerName is the member's username, kept as an annotation for admins (the owner label is
	// their stable id, which says nothing to a person).
	OwnerName string
	Image     config.Image
	Size      config.Size
	DiskGiB   int
	// PasswordHash is the SHA-512 crypt hash of the default user's password.
	PasswordHash string
}

const idAlphabet = "abcdefghijkmnpqrstuvwxyz23456789" // no 0/o, 1/l

var idFormat = regexp.MustCompile(`^vm-[a-km-np-z2-9]{6}$`)

// newID is "vm-" and 6 random characters: names are the service's, display names the member's.
func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	for i := range b {
		b[i] = idAlphabet[int(b[i])%len(idAlphabet)]
	}
	return "vm-" + string(b)
}

// HashPassword is the SHA-512 crypt hash cloud-init's chpasswd takes, with a random salt.
func HashPassword(password string) (string, error) {
	return sha512_crypt.New().Generate([]byte(password), nil)
}

func diskName(id string) string   { return id + "-root" }
func secretName(id string) string { return id + "-init" }

// buildVM is the VirtualMachine for a request: run strategy Manual (it starts only when asked to,
// and a shutdown from inside the guest leaves it stopped), with its root disk cloned from the image's
// source PVC in the same namespace, cloud-init from the Secret secretName(id), the pod network
// (masquerade) and the default serial console and VNC display.
func buildVM(namespace, id, owner, storageClass string, r Request) *kubevirtv1.VirtualMachine {
	manual := kubevirtv1.RunStrategyManual
	labels := map[string]string{LabelOwner: owner, LabelMachine: id}
	memory := resource.MustParse(strconv.Itoa(r.Size.MemoryMiB) + "Mi")
	storage := &cdiv1.StorageSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceStorage: resource.MustParse(strconv.Itoa(r.DiskGiB) + "Gi"),
		}},
	}
	if storageClass != "" {
		storage.StorageClassName = &storageClass
	}
	return &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name: id, Namespace: namespace, Labels: labels,
			Annotations: map[string]string{
				AnnotationName: r.Name,
				AnnotationUser: r.OwnerName,
				AnnotationImg:  r.Image.ID,
				AnnotationSize: r.Size.ID,
				AnnotationCPU:  strconv.Itoa(r.Size.CPU),
				AnnotationMem:  strconv.Itoa(r.Size.MemoryMiB),
				AnnotationDisk: strconv.Itoa(r.DiskGiB),
			},
		},
		Spec: kubevirtv1.VirtualMachineSpec{
			RunStrategy: &manual,
			DataVolumeTemplates: []kubevirtv1.DataVolumeTemplateSpec{{
				ObjectMeta: metav1.ObjectMeta{Name: diskName(id), Labels: labels},
				Spec: cdiv1.DataVolumeSpec{
					Source:  &cdiv1.DataVolumeSource{PVC: &cdiv1.DataVolumeSourcePVC{Namespace: namespace, Name: r.Image.Source}},
					Storage: storage,
				},
			}},
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: kubevirtv1.VirtualMachineInstanceSpec{
					Domain: kubevirtv1.DomainSpec{
						CPU:    &kubevirtv1.CPU{Cores: uint32(r.Size.CPU)},
						Memory: &kubevirtv1.Memory{Guest: &memory},
						Resources: kubevirtv1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceMemory: memory},
						},
						Devices: kubevirtv1.Devices{
							Disks: []kubevirtv1.Disk{
								{Name: "root", DiskDevice: kubevirtv1.DiskDevice{Disk: &kubevirtv1.DiskTarget{Bus: kubevirtv1.DiskBusVirtio}}},
								{Name: "cloudinit", DiskDevice: kubevirtv1.DiskDevice{Disk: &kubevirtv1.DiskTarget{Bus: kubevirtv1.DiskBusVirtio}}},
							},
							Interfaces: []kubevirtv1.Interface{{
								Name:                   "default",
								InterfaceBindingMethod: kubevirtv1.InterfaceBindingMethod{Masquerade: &kubevirtv1.InterfaceMasquerade{}},
							}},
						},
					},
					Networks: []kubevirtv1.Network{{Name: "default", NetworkSource: kubevirtv1.NetworkSource{Pod: &kubevirtv1.PodNetwork{}}}},
					Volumes: []kubevirtv1.Volume{
						{Name: "root", VolumeSource: kubevirtv1.VolumeSource{DataVolume: &kubevirtv1.DataVolumeSource{Name: diskName(id)}}},
						{Name: "cloudinit", VolumeSource: kubevirtv1.VolumeSource{CloudInitNoCloud: &kubevirtv1.CloudInitNoCloudSource{
							UserDataSecretRef: &corev1.LocalObjectReference{Name: secretName(id)},
						}}},
					},
				},
			},
		},
	}
}

// cloudConfig is the machine's #cloud-config: the image's extras, then what the service sets,
// which wins: the hostname, the default user's password (as a hash) and no SSH password logins.
func cloudConfig(id string, r Request) ([]byte, error) {
	doc := map[string]any{}
	for k, v := range r.Image.CloudConfig {
		doc[k] = v
	}
	doc["hostname"] = id
	doc["ssh_pwauth"] = false
	doc["chpasswd"] = map[string]any{
		"expire": false,
		"users":  []any{map[string]any{"name": r.Image.DefaultUser, "password": r.PasswordHash, "type": "hash"}},
	}
	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return append([]byte("#cloud-config\n"), body...), nil
}

// buildSecret is the cloud-init Secret, owned by the VM so it goes when the VM does.
func buildSecret(vm *kubevirtv1.VirtualMachine, userData []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: secretName(vm.Name), Namespace: vm.Namespace,
			Labels: map[string]string{LabelOwner: vm.Labels[LabelOwner], LabelMachine: vm.Name},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: kubevirtv1.SchemeGroupVersion.String(), Kind: "VirtualMachine", Name: vm.Name, UID: vm.UID,
			}},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"userdata": userData},
	}
}

func annotationInt(vm *kubevirtv1.VirtualMachine, key string) int {
	n, _ := strconv.Atoi(vm.Annotations[key])
	return n
}

// toMachine is the API's view of a VM. disk is its root DataVolume, if known (for clone progress).
func toMachine(vm *kubevirtv1.VirtualMachine, disk *cdiv1.DataVolume, catalog *config.Catalog) Machine {
	m := Machine{
		ID:        vm.Name,
		Name:      vm.Annotations[AnnotationName],
		Image:     vm.Annotations[AnnotationImg],
		Size:      vm.Annotations[AnnotationSize],
		CPU:       annotationInt(vm, AnnotationCPU),
		MemoryMiB: annotationInt(vm, AnnotationMem),
		DiskGiB:   annotationInt(vm, AnnotationDisk),
		CreatedAt: vm.CreationTimestamp.UTC(),
	}
	m.ImageName = m.Image // an image since removed from the catalog shows its id
	if img, ok := catalog.Image(m.Image); ok {
		m.ImageName, m.DefaultUser = img.Name, img.DefaultUser
	}
	m.State, m.Detail = state(vm, disk)
	return m
}

// state maps KubeVirt's printable status onto the API's few states.
func state(vm *kubevirtv1.VirtualMachine, disk *cdiv1.DataVolume) (string, string) {
	if vm.DeletionTimestamp != nil {
		return "Deleting", ""
	}
	s := vm.Status.PrintableStatus
	switch s {
	case kubevirtv1.VirtualMachineStatusStopped:
		return "Stopped", ""
	case kubevirtv1.VirtualMachineStatusProvisioning, kubevirtv1.VirtualMachineStatusWaitingForVolumeBinding, "":
		detail := ""
		if disk != nil && disk.Status.Progress != "" && disk.Status.Progress != "N/A" {
			detail = fmt.Sprintf("Copying the disk: %s", disk.Status.Progress)
		}
		return "Provisioning", detail
	case kubevirtv1.VirtualMachineStatusStarting, kubevirtv1.VirtualMachineStatusMigrating:
		return "Starting", ""
	case kubevirtv1.VirtualMachineStatusRunning:
		return "Running", ""
	case kubevirtv1.VirtualMachineStatusPaused:
		return "Running", "Paused"
	case kubevirtv1.VirtualMachineStatusStopping, kubevirtv1.VirtualMachineStatusTerminating:
		return "Stopping", ""
	}
	detail := string(s)
	for _, c := range vm.Status.Conditions {
		if c.Status == corev1.ConditionFalse && c.Type == kubevirtv1.VirtualMachineReady && c.Message != "" {
			detail += ": " + c.Message
			break
		}
		if c.Type == kubevirtv1.VirtualMachineFailure && c.Message != "" {
			detail += ": " + c.Message
			break
		}
	}
	return "Error", detail
}
