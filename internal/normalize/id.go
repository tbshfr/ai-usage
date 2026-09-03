package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// ErrMissingSpanIDs marks malformed generation spans whose trace/span IDs
// are empty, so no dedup key can be derived. Callers classify it to count
// normalization errors by reason.
var ErrMissingSpanIDs = fmt.Errorf("empty trace/span ID, cannot derive dedup key")

// DedupID derives the deterministic record ID per D2:
// sha256hex("<source>|" + traceID + "|" + spanID).
func DedupID(source, traceID, spanID string) (string, error) {
	if traceID == "" || spanID == "" {
		return "", ErrMissingSpanIDs
	}
	sum := sha256.Sum256([]byte(source + "|" + traceID + "|" + spanID))
	return hex.EncodeToString(sum[:]), nil
}
