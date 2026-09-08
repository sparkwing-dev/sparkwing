package sessionledger

import (
	"crypto/rand"
	"encoding/hex"
)

func randomLedgerSuffix() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
