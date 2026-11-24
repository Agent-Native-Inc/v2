package store

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// copyReason ends the "copying N…" line. exFAT copies too, but an SD card is the usual reason.
const copyReason = " from another disk"

// Clone makes dst share src's disk blocks (instant, no extra space). That only works
// within one APFS volume, so across disks, or on exFAT, it falls back to a real copy.
func Clone(src, dst string) error {
	err := unix.Clonefile(src, dst, 0)
	if !errors.Is(err, syscall.EXDEV) && !errors.Is(err, syscall.ENOTSUP) {
		return err // nil, or a real problem (missing file, no permission, dst exists)
	}
	return copyFile(src, dst)
}
