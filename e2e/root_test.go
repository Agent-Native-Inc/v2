package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitRefusesBroadFolders(t *testing.T) {
	home := t.TempDir()
	env := []string{"HOME=" + home}
	for _, d := range []string{"", "Desktop", "Documents", "Downloads"} {
		dir := filepath.Join(home, d)
		must(t, os.MkdirAll(dir, 0o755))
		if out, code := v2(t, dir, env, "init"); code == 0 || !strings.Contains(out, "won't track all of") || exists(filepath.Join(dir, ".git")) {
			t.Fatalf("init in ~/%s: exit %d: %s", d, code, out)
		}
	}
	if out, code := v2(t, os.TempDir(), env, "init"); code == 0 || !strings.Contains(out, "won't track all of") {
		t.Fatalf("init in the temp folder: exit %d: %s", code, out)
	}
	must(t, os.Symlink(home, filepath.Join(home, "Documents", "home-link")))
	for _, d := range []string{"Documents/home-link", "desktop"} {
		dir := filepath.Join(home, d)
		if _, err := os.Stat(dir); err != nil {
			continue // desktop: a case-sensitive disk, where it's another folder
		}
		if out, code := v2(t, dir, env, "init"); code == 0 || !strings.Contains(out, "won't track all of") {
			t.Fatalf("init in ~/%s: exit %d: %s", d, code, out)
		}
	}
	if out, code := v2(t, home, []string{"HOME=" + home + "/"}, "init"); code == 0 {
		t.Fatalf("HOME with a trailing slash: exit %d: %s", code, out)
	}
	for _, d := range []string{"trailer", "Documents/trailer"} {
		dir := filepath.Join(home, d)
		must(t, os.MkdirAll(dir, 0o755))
		if out, code := v2(t, dir, env, "init"); code != 0 {
			t.Fatalf("init in ~/%s: exit %d: %s", d, code, out)
		}
	}
}
