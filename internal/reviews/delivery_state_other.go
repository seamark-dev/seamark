//go:build !unix

package reviews

import (
	"fmt"
	"os"
	"time"
)

const deliveryNoFollow = 0

func lockDeliveryFile(*os.File) error {
	return fmt.Errorf("once-per-context delivery state locking is unsupported on this platform")
}

// lockDeliveryFileWithin refuses at once, as lockDeliveryFile does. A
// wait cannot add a lock that this platform does not have.
func lockDeliveryFileWithin(f *os.File, _ time.Duration) error {
	return lockDeliveryFile(f)
}

func unlockDeliveryFile(*os.File) error { return nil }
