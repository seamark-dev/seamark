//go:build unix

package lessoncheck

import (
	"io/fs"
	"os"
	"syscall"
)

// openFlags are the flags of every worktree file open. O_NOFOLLOW makes
// the open fail on a symbolic link, so a link that appears between the
// Lstat and the open cannot redirect the read. O_NONBLOCK keeps the
// open from blocking on a FIFO that appears at the same time. For a
// regular file, O_NONBLOCK has no effect.
const openFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// inodeOf returns the inode number of a file, for the revalidation
// identity. The conversion stays, because the type of Ino is not uint64
// on every unix platform.
func inodeOf(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}

	return 0
}
