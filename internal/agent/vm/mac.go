package vm

import (
	"crypto/sha256"
	"fmt"
)

// generateDeterministicMAC returns a MAC address AA:FC:xx:xx:xx:xx derived from vmID.
func generateDeterministicMAC(vmID string) string {
	h := sha256.Sum256([]byte(vmID))
	return fmt.Sprintf("AA:FC:%02X:%02X:%02X:%02X", h[0], h[1], h[2], h[3])
}
