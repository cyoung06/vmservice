package inspect

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// iso builds a minimal ISO 9660 image: a primary volume descriptor with label, a root directory
// with the given entries, and optionally a UDF NSR02 descriptor.
func iso(label string, names []string, udf bool) []byte {
	img := make([]byte, 40*sector)
	pvd := img[16*sector:]
	pvd[0] = 1
	copy(pvd[1:6], "CD001")
	copy(pvd[40:72], label+string(bytes.Repeat([]byte(" "), 32-len(label))))
	// root directory record at 156: extent at sector 30, one sector long
	root := pvd[156:]
	root[0] = 34
	binary.LittleEndian.PutUint32(root[2:6], 30)
	binary.LittleEndian.PutUint32(root[10:14], sector)
	root[25] = 2
	root[32] = 1
	next := 17
	if udf {
		d := img[next*sector:]
		copy(d[1:6], "BEA01")
		next++
		d = img[next*sector:]
		copy(d[1:6], "NSR02")
	}
	dir := img[30*sector:]
	off := 0
	for _, n := range append([]string{"\x00", "\x01"}, names...) {
		rec := 33 + len(n)
		if rec%2 == 1 {
			rec++
		}
		dir[off] = byte(rec)
		dir[off+32] = byte(len(n))
		copy(dir[off+33:], n)
		off += rec
	}
	return img
}

func TestISO(t *testing.T) {
	for name, c := range map[string]struct {
		img     []byte
		profile string
	}{
		"windows by label":  {iso("CCCOMA_X64FRE_EN-US_DV9", []string{"README.TXT;1"}, true), ProfileWin},
		"windows by readme": {iso("WHATEVER", []string{"README.TXT;1"}, true), ProfileWin},
		"windows by files":  {iso("X", []string{"BOOTMGR;1", "SOURCES", "EFI"}, false), ProfileWin},
		"ubuntu":            {iso("Ubuntu-Server 24.04", []string{"CASPER", "BOOT", "EFI", ".DISK"}, false), ProfileLin},
		"debian":            {iso("Debian 13", []string{"ISOLINUX", "INSTALL.AMD", "EFI"}, false), ProfileLin},
		"something else":    {iso("TOOLS", []string{"DATA.BIN;1"}, false), ProfileUEFI},
	} {
		r, err := File(bytes.NewReader(c.img), int64(len(c.img)))
		if err != nil || r.Kind != KindISO || r.Profile != c.profile || r.VirtualBytes != int64(len(c.img)) {
			t.Errorf("%s: %+v, %v; want iso/%s", name, r, err, c.profile)
		}
	}
}

func TestDiskHeaders(t *testing.T) {
	q := make([]byte, 64)
	copy(q, "QFI\xfb")
	binary.BigEndian.PutUint64(q[24:], 20<<30)
	if r, err := Header(q, 700<<20); err != nil || r.Kind != KindDisk || r.Format != "qcow2" || r.VirtualBytes != 20<<30 {
		t.Errorf("qcow2 = %+v, %v", r, err)
	}
	v := make([]byte, 64)
	copy(v, "KDMV")
	binary.LittleEndian.PutUint64(v[12:], (8<<30)/512)
	if r, err := Header(v, 1<<30); err != nil || r.Format != "vmdk" || r.VirtualBytes != 8<<30 {
		t.Errorf("vmdk = %+v, %v", r, err)
	}
	if _, err := Header([]byte("just some text file contents"), 30); err != ErrUnknown {
		t.Errorf("text = %v, want ErrUnknown", err)
	}
}

func TestRawDiskProfiles(t *testing.T) {
	bios := make([]byte, 64<<10)
	bios[510], bios[511] = 0x55, 0xAA
	bios[446+4] = 0x83 // a Linux partition
	if r, err := File(bytes.NewReader(bios), int64(len(bios))); err != nil || r.Kind != KindDisk || r.Profile != ProfileBIOS {
		t.Errorf("MBR disk = %+v, %v", r, err)
	}
	gpt := make([]byte, 64<<10)
	gpt[510], gpt[511] = 0x55, 0xAA
	gpt[446+4] = 0xEE // protective MBR
	h := gpt[512:]
	copy(h, "EFI PART")
	binary.LittleEndian.PutUint64(h[72:], 2) // entries at LBA 2
	binary.LittleEndian.PutUint32(h[80:], 4)
	binary.LittleEndian.PutUint32(h[84:], 128)
	copy(gpt[2*512+128:], espGUID) // the second entry is the ESP
	if r, err := File(bytes.NewReader(gpt), int64(len(gpt))); err != nil || r.Profile != ProfileUEFI {
		t.Errorf("GPT disk with ESP = %+v, %v", r, err)
	}
}

func TestProbe(t *testing.T) {
	img := iso("Ubuntu", []string{"CASPER"}, false)
	for name, h := range map[string]http.HandlerFunc{
		"range": func(w http.ResponseWriter, r *http.Request) {
			http.ServeContent(w, r, "x.iso", time.Time{}, bytes.NewReader(img))
		},
		"no range": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(img)))
			w.Write(img)
		},
	} {
		srv := httptest.NewServer(h)
		r, err := Probe(context.Background(), srv.URL)
		srv.Close()
		if err != nil || r.Kind != KindISO || r.SizeBytes != int64(len(img)) {
			t.Errorf("%s: %+v, %v", name, r, err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer srv.Close()
	if _, err := Probe(context.Background(), srv.URL); err == nil {
		t.Error("a 404 was accepted")
	}
}
