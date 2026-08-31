package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// DedupID derives the deterministic record ID per D2:
// sha256hex("<source>|" + traceID + "|" + spanID).
func DedupID(source, traceID, spanID string) (string, error) {
	if traceID == "" || spanID == "" {
		return "", fmt.Errorf("empty trace/span ID, cannot derive dedup key")
	}
	sum := sha256.Sum256([]byte(source + "|" + traceID + "|" + spanID))
	return hex.EncodeToString(sum[:]), nil
}
