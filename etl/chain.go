package etl

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// ChainHash is the hash of an audit entry given the previous entry's hash.
// Changing, removing or reordering any earlier entry changes every later hash.
func ChainHash(e AuditEntry) string {
	s := fmt.Sprintf("%s|%d|%d|%s|%s|%s|%s|%s|%s", e.PrevHash, e.Seq, e.At.UnixNano(), e.Actor, e.Action, e.BatchID, e.SourceID, e.Detail, e.TraceID)
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
