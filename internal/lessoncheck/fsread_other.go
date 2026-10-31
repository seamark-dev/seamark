//go:build !unix

package lessoncheck

import (
	"io/fs"
	"os"
)

// openFlags are the flags of every worktree file open. The platform has
// no O_NOFOLLOW. The Lstat of the path and of each parent before the
// open still refuse a symbolic link, and the open checks the file
// identity after it opens the file.
const openFlags = os.O_RDONLY

// inodeOf returns 0, because the platform gives no inode in its file
// information. The revalidation identity then rests on the size, the
// mode, and the modification time.
func inodeOf(fs.FileInfo) uint64 {
	return 0
}
