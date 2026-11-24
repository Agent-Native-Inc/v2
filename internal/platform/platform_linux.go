package platform

import (
	"os"
	"os/exec"
	"runtime"
)

const (
	// ReleaseFile is a bare binary, not a zip: a fresh Linux box may have no unzip.
	ReleaseFile = "v2-linux-" + runtime.GOARCH
	// Opener opens a URL in the browser.
	Opener = "xdg-open"
)

var installers = []struct{ bin, install string }{
	{"apt", "apt install"},
	{"dnf", "dnf install"},
	{"pacman", "pacman -S"},
	{"apk", "apk add"},
	{"zypper", "zypper install"},
}

// InstallHint tells the user how to install pkg, as a sentence, with the package manager this machine has.
func InstallHint(pkg string) string {
	for _, m := range installers {
		if _, err := exec.LookPath(m.bin); err != nil {
			continue
		}
		cmd := m.install + " " + pkg
		if os.Geteuid() != 0 {
			cmd = "sudo " + cmd
		}
		return "Install " + pkg + " with `" + cmd + "`"
	}
	return "Install " + pkg + " with your package manager"
}
