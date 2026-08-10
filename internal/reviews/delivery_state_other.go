//go:build !unix

package reviews

import (
	"fmt"
	"os"
)

const deliveryNoFollow = 0

func lockDeliveryFile(*os.File) error {
	return fmt.Errorf("once-per-context delivery state locking is unsupported on this platform")
}

func unlockDeliveryFile(*os.File) error { return nil }
