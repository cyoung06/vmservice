// Package inspect tells what a member's file is: an ISO (an installer) or a disk image, how big a
// disk it needs, and which device profile suits it. It reads fixed binary structures only (ISO 9660
// and UDF volume descriptors, the root directory, qcow2/VMDK headers, MBR/GPT) and runs in a
// locked-down Job ("vmservice inspect"), never in the portal's own process, except for Header,
// which looks at the first 64 KiB the browser sends.
package inspect

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"regexp"
	"strings"
)

// Kinds and profiles.
const (
	KindISO     = "iso"
	KindDisk    = "disk"
	ProfileWin  = "windows" // UEFI + Secure Boot, TPM, Hyper-V, virtio driver CD, no cloud-init
	ProfileLin  = "linux"   // UEFI, cloud-init attached
	ProfileUEFI = "uefi"    // UEFI, nothing else assumed
	ProfileBIOS = "bios"    // legacy BIOS boot
)

// HeaderSize is how much of a file Header needs.
const HeaderSize = 64 << 10

// Result is what's known about a file.
type Result struct {
	Kind         string `json:"kind"`
	Format       string `json:"format"`       // iso, qcow2, vmdk, raw
	VirtualBytes int64  `json:"virtualBytes"` // the disk it holds (an ISO: its own size)
	SizeBytes    int64  `json:"sizeBytes"`    // the file
	Profile      string `json:"profile,omitempty"`
	Label        string `json:"label,omitempty"` // an ISO's volume label
}

// ErrUnknown is a file that's none of the supported formats.
var ErrUnknown = errors.New("this isn't an ISO or a disk image (qcow2, VMDK or raw)")

const sector = 2048

// Header classifies a file from its first bytes (up to HeaderSize) and its total size.
func Header(b []byte, total int64) (Result, error) {
	r := Result{SizeBytes: total, VirtualBytes: total}
	switch {
	case len(b) >= 16*sector+6 && string(b[16*sector+1:16*sector+6]) == "CD001":
		r.Kind, r.Format = KindISO, "iso"
		r.Label = strings.TrimSpace(string(b[16*sector+40 : 16*sector+72]))
	case len(b) >= 32 && string(b[:4]) == "QFI\xfb":
		r.Kind, r.Format = KindDisk, "qcow2"
		r.VirtualBytes = int64(binary.BigEndian.Uint64(b[24:32]))
	case len(b) >= 20 && string(b[:4]) == "KDMV":
		r.Kind, r.Format = KindDisk, "vmdk"
		r.VirtualBytes = int64(binary.LittleEndian.Uint64(b[12:20])) * 512
	case len(b) >= 520 && string(b[512:520]) == "EFI PART", len(b) >= 512 && b[510] == 0x55 && b[511] == 0xAA:
		r.Kind, r.Format = KindDisk, "raw"
	default:
		return r, ErrUnknown
	}
	if r.VirtualBytes <= 0 {
		return r, ErrUnknown
	}
	return r, nil
}

// File inspects a whole file (after import, CDI has turned disk images into raw disk.img): its kind
// and size, and a device profile.
func File(f io.ReaderAt, size int64) (Result, error) {
	head := make([]byte, HeaderSize)
	n, err := f.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return Result{}, err
	}
	r, err := Header(head[:n], size)
	if err != nil {
		return r, err
	}
	switch r.Kind {
	case KindISO:
		r.Profile = isoProfile(f, r.Label)
	case KindDisk:
		r.Profile = ProfileBIOS
		if hasESP(f) {
			r.Profile = ProfileUEFI
		}
	}
	return r, nil
}

// Windows install media: their volume labels (CCCOMA_X64FRE_EN-US_DV9, CPBA_…, SSS_X64FREE_…, …).
var windowsLabel = regexp.MustCompile(`^(CCCOMA|CPBA|CCSA|CENA|J_CCSA|SSS_|IR[0-9]|GRMC|GSP|HRM)`)

// isoProfile looks at an ISO's volume descriptors and root directory.
func isoProfile(f io.ReaderAt, label string) string {
	udf := false
	for s := int64(16); s < 64; s++ {
		id := make([]byte, 6)
		if _, err := f.ReadAt(id, s*sector); err != nil {
			break
		}
		switch string(id[1:6]) {
		case "NSR02", "NSR03":
			udf = true
		}
	}
	names := rootNames(f)
	has := func(n string) bool { _, ok := names[n]; return ok }
	switch {
	case windowsLabel.MatchString(label), has("SOURCES") && has("BOOTMGR"),
		// Windows ISOs are UDF; their ISO 9660 side often holds only a README saying so.
		udf && len(names) <= 2 && has("README.TXT"):
		return ProfileWin
	case has("CASPER"), has("LIVE"), has("ISOLINUX"), has(".DISK"), has("ARCH"), has("IMAGES") && has("EFI"), has("BOOT") && has("EFI"):
		return ProfileLin
	}
	return ProfileUEFI
}

// rootNames is the names in an ISO 9660 root directory, upper-cased, without ";1" versions.
func rootNames(f io.ReaderAt) map[string]struct{} {
	names := map[string]struct{}{}
	pvd := make([]byte, sector)
	if _, err := f.ReadAt(pvd, 16*sector); err != nil || string(pvd[1:6]) != "CD001" {
		return names
	}
	root := pvd[156 : 156+34]
	extent := int64(binary.LittleEndian.Uint32(root[2:6]))
	length := int64(binary.LittleEndian.Uint32(root[10:14]))
	if length <= 0 || length > 1<<20 {
		return names
	}
	dir := make([]byte, length)
	if _, err := f.ReadAt(dir, extent*sector); err != nil && !errors.Is(err, io.EOF) {
		return names
	}
	for off := 0; off < len(dir); {
		n := int(dir[off])
		if n == 0 { // records don't cross sectors: skip to the next one
			off = (off/sector + 1) * sector
			continue
		}
		if off+n > len(dir) || n < 34 {
			break
		}
		nameLen := int(dir[off+32])
		if 33+nameLen <= n {
			name := string(dir[off+33 : off+33+nameLen])
			if name != "\x00" && name != "\x01" {
				name, _, _ = strings.Cut(strings.ToUpper(name), ";")
				names[strings.TrimSuffix(name, ".")] = struct{}{}
			}
		}
		off += n
	}
	return names
}

// The EFI System Partition's type GUID as it's stored on disk.
var espGUID = []byte{0x28, 0x73, 0x2A, 0xC1, 0x1F, 0xF8, 0xD2, 0x11, 0xBA, 0x4B, 0x00, 0xA0, 0xC9, 0x3E, 0xC9, 0x3B}

// hasESP says whether a raw disk has a GPT with an EFI System Partition (or an MBR one, type 0xEF).
func hasESP(f io.ReaderAt) bool {
	lba := make([]byte, 1024)
	if _, err := f.ReadAt(lba, 0); err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	for i := 0; i < 4; i++ { // MBR partition types
		if lba[446+16*i+4] == 0xEF {
			return true
		}
	}
	gpt := lba[512:]
	if string(gpt[:8]) != "EFI PART" {
		return false
	}
	start := int64(binary.LittleEndian.Uint64(gpt[72:80])) * 512
	count := int(binary.LittleEndian.Uint32(gpt[80:84]))
	size := int(binary.LittleEndian.Uint32(gpt[84:88]))
	if size < 16 || count <= 0 || count > 1024 {
		return false
	}
	entries := make([]byte, count*size)
	if _, err := f.ReadAt(entries, start); err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	for i := 0; i < count; i++ {
		if bytes.Equal(entries[i*size:i*size+16], espGUID) {
			return true
		}
	}
	return false
}
