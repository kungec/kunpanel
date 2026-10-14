package util

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
)

const PBKDF2Iterations = 100000

// PBKDF2 实现 PBKDF2-HMAC-SHA256(RFC 2898),用于口令存储 KDF
func PBKDF2(password, salt []byte, iterations, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen

	var buf [4]byte
	dk := make([]byte, 0, numBlocks*hashLen)
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf[:], uint32(block))
		prf.Write(buf[:4])
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for n := 1; n < iterations; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for x := range t {
				t[x] ^= u[x]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}
