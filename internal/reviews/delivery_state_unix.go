//go:build unix

package reviews

import (
	"errors"
	"os"
	"syscall"
	"time"
)

const deliveryNoFollow = syscall.O_NOFOLLOW

// deliveryLockPoll is the interval between two attempts of
// lockDeliveryFileWithin. An attempt is one flock call, so a short
// interval costs little. A lease holds the lock for about 5 ms.
const deliveryLockPoll = 10 * time.Millisecond

func lockDeliveryFile(f *os.File) error {
	// Never queue an edit behind another hook process. Contention is a state
	// failure and the caller deliberately falls back to normal injection.
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// lockDeliveryFileWithin takes the lock as lockDeliveryFile does. When
// another process holds it, the function tries again until wait ends.
// Then it returns the busy error. Every other error returns at once,
// because a wait cannot repair it. A wait of zero is one attempt.
func lockDeliveryFileWithin(f *os.File, wait time.Duration) error {
	deadline := time.Now().Add(wait)

	for {
		err := lockDeliveryFile(f)
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}

		left := time.Until(deadline)
		if left <= 0 {
			return err
		}

		time.Sleep(min(deliveryLockPoll, left))
	}
}

func unlockDeliveryFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
