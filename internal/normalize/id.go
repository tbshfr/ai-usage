package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
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

// DedupLogID derives a stable content identity for log records that do not
// carry trace/span IDs. Length-prefixed identity fields and explicit nil
// markers keep distinct field combinations unambiguous before hashing.
func DedupLogID(source, conversationID string, timestamp time.Time, model string, tokens ...*int64) (string, error) {
	if source == "" || conversationID == "" || timestamp.IsZero() || model == "" {
		return "", ErrMissingLogIdentity
	}
	var input strings.Builder
	for _, part := range []string{source, conversationID, timestamp.UTC().Format(time.RFC3339Nano), model} {
		input.WriteString(strconv.Itoa(len(part)))
		input.WriteByte(':')
		input.WriteString(part)
	}
	for _, token := range tokens {
		input.WriteByte('|')
		if token == nil {
			input.WriteByte('n')
			continue
		}
		input.WriteByte('v')
		input.WriteString(strconv.FormatInt(*token, 10))
	}
	sum := sha256.Sum256([]byte(input.String()))
	return hex.EncodeToString(sum[:]), nil
}
