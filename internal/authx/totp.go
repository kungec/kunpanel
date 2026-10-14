// TOTP 两步验证(RFC 6238,SHA1/6位/30秒,兼容 Google Authenticator 等)
package authx

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// GenerateTOTPSecret 生成 base32 编码的 TOTP 密钥(无填充,兼容验证器 App 手动输入)
func GenerateTOTPSecret() (string, error) {
	key := make([]byte, 20)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key), nil
}

// Base32Decode 解码 base32(容忍大小写与填充差异)
func Base32Decode(s string) ([]byte, error) {
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(
		strings.ToUpper(strings.ReplaceAll(s, " ", "")))
}

func totpCode(secret []byte, t time.Time) string {
	counter := uint64(t.Unix() / 30)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, secret)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[off])&0x7f)<<24 |
		(uint32(sum[off+1])&0xff)<<16 |
		(uint32(sum[off+2])&0xff)<<8 |
		uint32(sum[off+3])&0xff
	return fmt.Sprintf("%06d", code%1000000)
}

// VerifyTOTP 校验 6 位动态码(允许前后各 30 秒偏移;常量时间比较)
func VerifyTOTP(secretB32, code string) bool {
	secret, err := Base32Decode(secretB32)
	if err != nil {
		return false
	}
	return VerifyTOTPSecretAt(secret, code, time.Now())
}

// VerifyTOTPSecretAt 在指定时间点校验(供测试与偏移容错)
func VerifyTOTPSecretAt(secret []byte, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	for _, skew := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		expect := totpCode(secret, now.Add(skew))
		if hmac.Equal([]byte(expect), []byte(code)) {
			return true
		}
	}
	return false
}
