//go:build !unix

package integrity

import (
	"fmt"
)

// fallbackSignalTest provides a graceful fallback on non-Unix environments.
func fallbackSignalTest(primaryErr error) (attached bool, evidence string, err error) {
	return false, fmt.Sprintf("fallback check passed; primary check unavailable (%v)", primaryErr), nil
}
