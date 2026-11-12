// Package config is the catalog file: the images members can pick, the sizes, the disk range and
// the per-member limits.
package config

import (
	"fmt"
	"os"
	"regexp"

	"sigs.k8s.io/yaml"
)

// Catalog is the -catalog file.
type Catalog struct {
	Images []Image `json:"images"`
	Sizes  []Size  `json:"sizes"`
	Disk   Disk    `json:"disk"`
	Limits Limits  `json:"limits"`
	// StorageClass is the class of the machines' disks; empty: the cluster's default.
	StorageClass string `json:"storageClass,omitempty"`
}

// Image is a golden disk in the namespace that new machines are cloned from.
type Image struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Source is the PVC (usually a DataVolume's) in the machines' namespace holding the image.
	Source string `json:"source"`
	// DefaultUser is the image's cloud-init default user, whose password the member sets.
	DefaultUser string `json:"defaultUser"`
	// CloudConfig is merged into the generated #cloud-config (e.g. apt mirrors). Keys the service
	// sets itself (hostname, chpasswd, ssh_pwauth) can't be overridden.
	CloudConfig map[string]any `json:"cloudConfig,omitempty"`
}

// Size is a CPU and memory preset.
type Size struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CPU       int    `json:"cpu"`
	MemoryMiB int    `json:"memoryMiB"`
}

// Disk is the range a member may pick a root disk size from.
type Disk struct {
	MinGiB     int `json:"minGiB"`
	MaxGiB     int `json:"maxGiB"`
	DefaultGiB int `json:"defaultGiB"`
}

// Limits is what one member's machines may add up to, running or not.
type Limits struct {
	VMs       int `json:"vms"`
	CPU       int `json:"cpu"`
	MemoryMiB int `json:"memoryMiB"`
	DiskGiB   int `json:"diskGiB"`
}

var id = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,61}[a-z0-9])?$`)

// Load reads and checks the catalog file.
func Load(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Catalog
	if err := yaml.UnmarshalStrict(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Catalog) check() error {
	if len(c.Images) == 0 || len(c.Sizes) == 0 {
		return fmt.Errorf("needs at least one image and one size")
	}
	seen := map[string]bool{}
	for _, i := range c.Images {
		if !id.MatchString(i.ID) || !id.MatchString(i.Source) || i.Name == "" || i.DefaultUser == "" {
			return fmt.Errorf("image %q: id and source must be DNS names; name and defaultUser are required", i.ID)
		}
		if seen["image/"+i.ID] {
			return fmt.Errorf("image %q is listed twice", i.ID)
		}
		seen["image/"+i.ID] = true
	}
	for _, s := range c.Sizes {
		if !id.MatchString(s.ID) || s.Name == "" || s.CPU < 1 || s.MemoryMiB < 256 {
			return fmt.Errorf("size %q: needs an id, a name, cpu >= 1 and memoryMiB >= 256", s.ID)
		}
		if seen["size/"+s.ID] {
			return fmt.Errorf("size %q is listed twice", s.ID)
		}
		seen["size/"+s.ID] = true
	}
	d := c.Disk
	if d.MinGiB < 1 || d.MaxGiB < d.MinGiB || d.DefaultGiB < d.MinGiB || d.DefaultGiB > d.MaxGiB {
		return fmt.Errorf("disk: needs 1 <= minGiB <= defaultGiB <= maxGiB")
	}
	l := c.Limits
	if l.VMs < 1 || l.CPU < 1 || l.MemoryMiB < 1 || l.DiskGiB < 1 {
		return fmt.Errorf("limits: vms, cpu, memoryMiB and diskGiB must all be set")
	}
	return nil
}

// Image returns the image with id, if any.
func (c *Catalog) Image(id string) (Image, bool) {
	for _, i := range c.Images {
		if i.ID == id {
			return i, true
		}
	}
	return Image{}, false
}

// Size returns the size with id, if any.
func (c *Catalog) Size(id string) (Size, bool) {
	for _, s := range c.Sizes {
		if s.ID == id {
			return s, true
		}
	}
	return Size{}, false
}
