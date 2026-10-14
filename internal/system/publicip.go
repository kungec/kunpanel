// system/publicip.go —— 公网 IP 探测:多源回退 + 每小时刷新
package system

import (
	"io"
	"net/http"
	"regexp"
	"sync/atomic"
	"time"
)

var publicIP atomic.Value // string

var ipv4Re = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`)

var publicIPSources = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://ip.3322.net",
	"https://members.3322.org/dyndns/getip",
	"https://myip.ipip.net",
}

func publicIPNow() string {
	client := &http.Client{Timeout: 5 * time.Second}
	for _, u := range publicIPSources {
		func() {
			req, e := http.NewRequest("GET", u, nil)
			if e != nil {
				return
			}
			req.Header.Set("User-Agent", "curl/8.0")
			resp, e := client.Do(req)
			if e != nil {
				return
			}
			defer resp.Body.Close()
			b, e := io.ReadAll(io.LimitReader(resp.Body, 256))
			if e != nil {
				return
			}
			if m := ipv4Re.FindString(string(b)); m != "" {
				publicIP.Store(m)
			}
		}()
		if v, ok := publicIP.Load().(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// PublicIPValue 返回当前缓存的公网 IP(未获取到则为空)
func PublicIPValue() string {
	v, _ := publicIP.Load().(string)
	return v
}

// startPublicIPFetcher 启动时探测一次,之后每小时刷新(公网 IP 变化可感知)
func startPublicIPFetcher(stop <-chan struct{}) {
	go func() {
		publicIPNow()
		tick := time.NewTicker(time.Hour)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				publicIPNow()
			}
		}
	}()
}
