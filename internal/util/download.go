package util

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

// privateIP 判断 IP 是否环回/私网/链路本地/未指定(SSRF 黑名单)
func privateIP(ip net.IP) bool {
	return ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// ValidatePublicURL 解析 URL 并确认主机不指向内网
func ValidatePublicURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL 缺少主机名")
	}
	if host == "localhost" {
		return fmt.Errorf("拒绝内网地址 localhost")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("解析主机失败: %v", err)
	}
	for _, ip := range ips {
		if privateIP(ip) {
			return fmt.Errorf("拒绝内网地址(%s)", ip)
		}
	}
	return nil
}

// PublicHTTPClient 返回仅允许访问公网地址的 HTTP 客户端:
// 预解析并校验目标 IP,后续拨号固定用该 IP(TLS SNI/Host 头保持原域名),
// 杜绝"校验时公网、拨号时被 DNS 重绑定到内网"的绕过。
func PublicHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.LookupIP(host)
				if err != nil {
					return nil, fmt.Errorf("解析主机失败: %v", err)
				}
				for _, ip := range ips {
					if privateIP(ip) {
						return nil, fmt.Errorf("拒绝内网地址(%s)", ip)
					}
				}
				// 固定用首个公网 IP 拨号,后续请求不再二次解析
				return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return fmt.Errorf("重定向次数过多")
			}
			return ValidatePublicURL(req.URL.String())
		},
	}
}

// SecureDownload 带重定向校验的下载:每一跳重新解析并拒绝内网地址,
// 写盘限 maxSize 字节,进度每 2 秒写入 progress(可为 nil)。
func SecureDownload(raw, dst string, progress io.Writer, timeout time.Duration, maxSize int64) error {
	if err := ValidatePublicURL(raw); err != nil {
		return err
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return fmt.Errorf("重定向次数过多")
			}
			return ValidatePublicURL(req.URL.String())
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "kunpanel-downloader")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	var done int64
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	buf := make([]byte, 128<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			done += int64(n)
			if done > maxSize {
				out.Close()
				return fmt.Errorf("超过大小上限(%d MB)", maxSize>>20)
			}
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		select {
		case <-ticker.C:
			if progress != nil {
				fmt.Fprintf(progress, "  已下载 %.1f MB\n", float64(done)/1024/1024)
			}
		default:
		}
	}
	if done < 1024 {
		return fmt.Errorf("下载内容异常(小于 1KB)")
	}
	return nil
}
