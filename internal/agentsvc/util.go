package agentsvc

import (
	"crypto/rand"
	"encoding/hex"
)

// newAgentID 生成 32 位 hex ID（与 handler.generateID 同语义）。
func newAgentID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
