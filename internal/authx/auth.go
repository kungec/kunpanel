package authx

import (
	"crypto/subtle"
	"fmt"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/util"
)

// NewToken 生成 HMAC 签名的访问令牌:"username.exp.signature"
func NewToken(secret, username string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("%s.%d", username, exp)
	return payload + "." + util.HMAC(secret, payload)
}

// VerifyToken 校验令牌:格式、签名(常量时间)、有效期、用户名
func VerifyToken(secret, token, username string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	u, ts, sig := parts[0], parts[1], parts[2]
	var exp int64
	if _, err := fmt.Sscanf(ts, "%d", &exp); err != nil {
		return false
	}
	if time.Now().Unix() > exp {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(u), []byte(username)) != 1 {
		return false
	}
	expect := util.HMAC(secret, u+"."+ts)
	return subtle.ConstantTimeCompare([]byte(expect), []byte(sig)) == 1
}

// Limiter 登录失败限流:同一 IP 连续失败 5 次,锁定 10 分钟
type Limiter struct {
	mu       sync.Mutex
	fails    map[string]*failInfo
	maxFails int
	window   time.Duration
}

type failInfo struct {
	count    int
	until    time.Time
	lastSeen time.Time // 最后活跃时间:清理低频条目防 map 无限增长
}

func NewLimiter() *Limiter {
	return &Limiter{fails: map[string]*failInfo{}, window: 10 * time.Minute}
}

// NewLimiterOpt 自定义阈值与窗口(如用户名维度限流)
func NewLimiterOpt(maxFails int, window time.Duration) *Limiter {
	return &Limiter{maxFails: maxFails, fails: map[string]*failInfo{}, window: window}
}

func (l *Limiter) Blocked(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[ip]
	limit := l.maxFails
	if limit <= 0 {
		limit = 5
	}
	if !ok || f.count < limit {
		return false, 0
	}
	if time.Now().Before(f.until) {
		return true, time.Until(f.until)
	}
	delete(l.fails, ip)
	return false, 0
}

func (l *Limiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[ip]
	if !ok {
		f = &failInfo{}
		l.fails[ip] = f
	}
	f.count++
	f.lastSeen = time.Now()
	if f.count >= l.maxFails {
		f.until = time.Now().Add(l.window)
	}
	// 容量兜底:超上限时淘汰最久未活跃的条目
	const maxEntries = 10000
	if len(l.fails) > maxEntries {
		var oldestK string
		var oldestT time.Time
		first := true
		for k, v := range l.fails {
			if first || v.lastSeen.Before(oldestT) {
				oldestK, oldestT, first = k, v.lastSeen, false
			}
		}
		delete(l.fails, oldestK)
	}
}

func (l *Limiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}

// Sweep 清理已过期且不再活跃的条目(长期运行防 map 缓慢增长)
func (l *Limiter) Sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for k, f := range l.fails {
		// 达到封禁阈值且已过期 → 清;或 24h 无活跃 → 清(此前低频条目永不回收)
		if (f.count >= l.maxFails && now.After(f.until)) || now.Sub(f.lastSeen) > 24*time.Hour {
			delete(l.fails, k)
		}
	}
}
