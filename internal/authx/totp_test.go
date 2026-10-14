package authx

import (
	"testing"
	"time"
)

// RFC 6238 标准测试向量(SHA1,ASCII secret "12345678901234567890",取 31 位值后 6 位)
func TestTOTPRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	cases := []struct {
		sec  int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	}
	for _, c := range cases {
		if got := totpCode(secret, time.Unix(c.sec, 0)); got != c.want {
			t.Errorf("t=%d got %s want %s", c.sec, got, c.want)
		}
	}
}

// 偏移容错与常量时间比较
func TestVerifyTOTPSecretAt(t *testing.T) {
	secret := []byte("12345678901234567890")
	if !VerifyTOTPSecretAt(secret, "287082", time.Unix(59, 0)) {
		t.Fatal("精确时间校验失败")
	}
	if !VerifyTOTPSecretAt(secret, "287082", time.Unix(89, 0)) { // +30s 容差
		t.Fatal("容差窗口校验失败")
	}
	if VerifyTOTPSecretAt(secret, "000000", time.Unix(59, 0)) {
		t.Fatal("错误动态码不应通过")
	}
	if VerifyTOTPSecretAt(secret, "28708", time.Unix(59, 0)) {
		t.Fatal("长度错误不应通过")
	}
}
