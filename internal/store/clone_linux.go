package store

import (
	"os"

	"golang.org/x/sys/unix"
)

// copyReason is empty: most Linux disks can't clone at all, so "from another disk" would mislead.
const copyReason = ""

// Clone makes dst share src's disk blocks on filesystems that can (btrfs, XFS). Anywhere else,
// ext4 included, or across disks, it falls back to a real copy. Either way dst keeps src's
// permissions, as clonefile does on the Mac, so a restored tool stays executable.
func Clone(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	err = unix.IoctlFileClone(int(out.Fd()), int(in.Fd()))
	out.Close()
	if err != nil {
		os.Remove(dst) // copy on any FICLONE error: copyFile reports a real problem itself
		if err := copyFile(src, dst); err != nil {
			return err
		}
	}
	return os.Chmod(dst, fi.Mode().Perm())
}
