package policy

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// SourceDigest identifies the actual bytes compiled by LoadSplitWith. It is
// local activation evidence only, never a snapshot field or a log value.
func SourceDigest(policyData, handsetsData []byte) string {
	h := sha256.New()
	for _, data := range [][]byte{policyData, handsetsData} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(data)))
		h.Write(size[:])
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (p *Policy) SourceDigest() string { return p.sourceDigest }
