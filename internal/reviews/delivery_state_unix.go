//go:build unix

package reviews

import (
	"os"
	"syscall"
)

const deliveryNoFollow = syscall.O_NOFOLLOW

func lockDeliveryFile(f *os.File) error {
	// Never queue an edit behind another hook process. Contention is a state
	// failure and the caller deliberately falls back to normal injection.
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockDeliveryFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
