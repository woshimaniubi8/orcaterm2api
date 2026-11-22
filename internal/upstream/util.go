package upstream

import (
	"crypto/rand"
	"encoding/hex"
)

// newRequestID 生成 32 位十六进制随机 ID，用于 input.id / x-seqid / X-Request-Id。
// 上游要求 input.id 必须是合法 UUID；这里直接输出标准 8-4-4-4-12 形态。
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := make([]byte, 32)
	hex.Encode(h, b[:])
	return string(h[0:8]) + "-" + string(h[8:12]) + "-" + string(h[12:16]) + "-" + string(h[16:20]) + "-" + string(h[20:32])
}
