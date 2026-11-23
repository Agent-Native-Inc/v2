package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeRandom(t *testing.T, path string, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	rand.Read(b)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

type asset struct {
	Sha256 string `json:"sha256"`
	Source string `json:"source"`
	Size   int64  `json:"size"`
}

func assets(t *testing.T, dir string) map[string]asset {
	t.Helper()
	m := map[string]asset{}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(dir, ".v2/assets.json"))), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// checkImported verifies the file landed with the right bytes, is recorded, and is in the store.
func checkImported(t *testing.T, dir, key string, want []byte) {
	t.Helper()
	if got := read(t, filepath.Join(dir, key)); got != string(want) {
		t.Fatalf("%s has the wrong bytes", key)
	}
	sum := sha256.Sum256(want)
	hex := hex.EncodeToString(sum[:])
	a, ok := assets(t, dir)[key]
	if !ok || a.Sha256 != hex || a.Size != int64(len(want)) || a.Source != "imported" {
		t.Fatalf("assets.json[%s] = %+v, want sha %s", key, a, hex)
	}
	if obj, _ := os.ReadFile(filepath.Join(dir, ".v2/objects", hex[:2], hex[2:])); !bytes.Equal(obj, want) {
		t.Fatalf("object for %s missing or wrong", key)
	}
}

func newProject(t *testing.T, dir string) {
	t.Helper()
	if out, code := v2(t, dir, nil, "init"); code != 0 {
		t.Fatalf("init: %s", out)
	}
}

func TestImportSameDisk(t *testing.T) {
	proj, footage := t.TempDir(), t.TempDir()
	newProject(t, proj)
	want := writeRandom(t, filepath.Join(footage, "clip.mp4"), 1<<20)

	out, code := v2(t, proj, nil, "import", filepath.Join(footage, "clip.mp4"))
	if code != 0 || strings.Contains(out, "copying") {
		t.Fatalf("exit %d: %s", code, out)
	}
	checkImported(t, proj, "assets/imported/clip.mp4", want)
}

func TestImportNameCollision(t *testing.T) {
	proj, camA, camB := t.TempDir(), t.TempDir(), t.TempDir()
	newProject(t, proj)
	a := writeRandom(t, filepath.Join(camA, "C0001.MP4"), 1000)
	b := writeRandom(t, filepath.Join(camB, "C0001.MP4"), 1000) // same name and size, different bytes
	v2(t, proj, nil, "import", filepath.Join(camA, "C0001.MP4"))

	out, code := v2(t, proj, nil, "import", filepath.Join(camB, "C0001.MP4"))
	if code != 1 || !strings.Contains(out, "--as") {
		t.Fatalf("collision: exit %d: %s", code, out)
	}
	checkImported(t, proj, "assets/imported/C0001.MP4", a) // untouched

	if out, code := v2(t, proj, nil, "import", filepath.Join(camB, "C0001.MP4"), "--as", "camB.MP4"); code != 0 {
		t.Fatalf("--as: exit %d: %s", code, out)
	}
	checkImported(t, proj, "assets/imported/camB.MP4", b)

	// Same bytes again is fine: nothing changes.
	if out, code := v2(t, proj, nil, "import", filepath.Join(camA, "C0001.MP4")); code != 0 {
		t.Fatalf("re-import: exit %d: %s", code, out)
	}
	checkImported(t, proj, "assets/imported/C0001.MP4", a)

	if _, code := v2(t, proj, nil, "import", "x", "y", "--as", "z"); code != 1 {
		t.Fatal("--as with two files should fail")
	}
	if _, code := v2(t, proj, nil, "import", filepath.Join(camA, "C0001.MP4"), "--as", "../escape.mp4"); code != 1 {
		t.Fatal("--as with a folder should fail")
	}
}

func TestImportOutsideProject(t *testing.T) {
	footage := t.TempDir()
	writeRandom(t, filepath.Join(footage, "clip.mp4"), 10)
	if out, code := v2(t, t.TempDir(), nil, "import", filepath.Join(footage, "clip.mp4")); code != 1 || !strings.Contains(out, "v2 init") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

// disk is the filesystem a test's disk image gets on each OS.
type disk struct{ mac, linux string }

var (
	otherDisk = disk{"APFS", "ext4"}   // a second disk, so a clone from the project's disk fails with EXDEV
	noClones  = disk{"ExFAT", "exfat"} // nothing can be cloned (ENOTSUP), not even within the disk
	twoSecond = disk{"MS-DOS", "vfat"} // FAT: file times step by 2 seconds, and a rewritten file keeps its inode
)

// mountDisk attaches a fresh disk image and returns its mount point.
func mountDisk(t *testing.T, d disk) string {
	t.Helper()
	if runtime.GOOS == "linux" {
		return loopMount(t, d.linux)
	}
	img := filepath.Join(t.TempDir(), "disk.dmg")
	name := "v2test" + strings.ToLower(d.mac)
	if out, err := exec.Command("hdiutil", "create", "-size", "400m", "-fs", d.mac, "-volname", name, img).CombinedOutput(); err != nil {
		t.Skipf("hdiutil create: %s", out)
	}
	out, err := exec.Command("hdiutil", "attach", img, "-nobrowse").CombinedOutput()
	if err != nil {
		t.Fatalf("attach: %s", out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	mnt := strings.TrimSpace(lines[len(lines)-1][strings.LastIndex(lines[len(lines)-1], "\t")+1:])
	t.Cleanup(func() {
		if out, err := exec.Command("hdiutil", "detach", mnt, "-force").CombinedOutput(); err != nil {
			t.Errorf("detach %s: %s", mnt, out)
		}
	})
	return mnt
}

func loopMount(t *testing.T, fs string) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("mounting a disk image on Linux needs root")
	}
	img, mnt := filepath.Join(t.TempDir(), "disk.img"), t.TempDir()
	if err := os.WriteFile(img, nil, 0o644); err != nil || os.Truncate(img, 400<<20) != nil {
		t.Fatal("can't make the disk image")
	}
	for _, args := range [][]string{{"mkfs", "-t", fs, img}, {"mount", "-o", "loop", img, mnt}} {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}
	t.Cleanup(func() {
		if out, err := exec.Command("umount", mnt).CombinedOutput(); err != nil {
			t.Errorf("umount %s: %s", mnt, out)
		}
	})
	return mnt
}

// External drive → laptop project: clone fails with EXDEV, so it must copy and say so.
func TestImportFromOtherDisk(t *testing.T) {
	if testing.Short() {
		t.Skip("mounts a disk image")
	}
	drive := mountDisk(t, otherDisk)
	proj := t.TempDir()
	newProject(t, proj)
	want := writeRandom(t, filepath.Join(drive, "big.mov"), 110<<20)

	out, code := v2(t, proj, nil, "import", filepath.Join(drive, "big.mov"))
	copying := map[string]string{"darwin": "copying 110 MB from another disk…", "linux": "copying 110 MB…"}[runtime.GOOS]
	if code != 0 || !strings.Contains(out, copying) || !strings.Contains(out, "100%") {
		t.Fatalf("exit %d: %s", code, out)
	}
	checkImported(t, proj, "assets/imported/big.mov", want)
}

// Project on an exFAT drive: nothing can be cloned (ENOTSUP), even inside the project.
func TestImportOnExFAT(t *testing.T) {
	if testing.Short() {
		t.Skip("mounts a disk image")
	}
	drive := mountDisk(t, noClones)
	proj := filepath.Join(drive, "proj")
	must(t, os.Mkdir(proj, 0o755))
	newProject(t, proj)
	want := writeRandom(t, filepath.Join(drive, "clip.mp4"), 1<<20)

	if out, code := v2(t, proj, nil, "import", filepath.Join(drive, "clip.mp4")); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	checkImported(t, proj, "assets/imported/clip.mp4", want)
}
