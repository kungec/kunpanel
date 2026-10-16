// Package nettools 网络工具箱:ping / 端口扫描 / DNS / HTTP 检测 / 路由追踪
package nettools

import (
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"kunpanel/internal/util"
)

// PingResult ping 结果
type PingResult struct {
	Target  string `json:"target"`
	Output  string `json:"output"`
	Success bool   `json:"success"`
}

// Ping 执行 ping
func Ping(host string, count int) (*PingResult, error) {
	host = sanitizeHost(host)
	if host == "" {
		return nil, fmt.Errorf("目标不能为空")
	}
	if count <= 0 || count > 20 {
		count = 4
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("ping", "-n", fmt.Sprint(count), host)
	} else {
		cmd = exec.Command("ping", "-c", fmt.Sprint(count), "-W", "3", host)
	}
	out, err := cmd.CombinedOutput()
	return &PingResult{Target: host, Output: string(out), Success: err == nil}, nil
}

// TraceResult 路由追踪结果
type TraceResult struct {
	Target string `json:"target"`
	Output string `json:"output"`
}

// TraceRoute 执行路由追踪
func TraceRoute(host string) (*TraceResult, error) {
	host = sanitizeHost(host)
	if host == "" {
		return nil, fmt.Errorf("目标不能为空")
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("tracert", host)
	} else if _, e := exec.LookPath("traceroute"); e == nil {
		cmd = exec.Command("traceroute", "-m", "15", "-w", "2", host)
	} else {
		cmd = exec.Command("tracepath", "-m", "15", host)
	}
	out, _ := util.RunWithTimeout(30*time.Second, cmd.Path, cmd.Args[1:]...)
	return &TraceResult{Target: host, Output: out}, nil
}

// DNSResult DNS 解析结果
type DNSResult struct {
	Target  string   `json:"target"`
	Records []string `json:"records"`
}

// DNSLookup DNS 解析(A / AAAA / CNAME)
func DNSLookup(host string) (*DNSResult, error) {
	host = sanitizeHost(host)
	if host == "" {
		return nil, fmt.Errorf("目标不能为空")
	}
	result := &DNSResult{Target: host}
	ips, err := net.LookupIP(host)
	if err != nil {
		return result, fmt.Errorf("DNS 解析失败: %v", err)
	}
	for _, ip := range ips {
		result.Records = append(result.Records, ip.String())
	}
	return result, nil
}

// PortScanResult 端口扫描结果
type PortScanResult struct {
	Target string      `json:"target"`
	Ports  []PortState `json:"ports"`
}

type PortState struct {
	Port  int    `json:"port"`
	State string `json:"state"` // open / closed
	Name  string `json:"name"`
}

var commonPorts = []int{21, 22, 23, 25, 53, 80, 110, 143, 443, 465, 587, 993, 995, 1433, 3306, 3389, 5432, 6379, 8080, 8443, 8888, 9090, 27017}

// PortScan 扫描常用端口
func PortScan(host string) (*PortScanResult, error) {
	host = sanitizeHost(host)
	if host == "" {
		return nil, fmt.Errorf("目标不能为空")
	}
	result := &PortScanResult{Target: host}
	for _, port := range commonPorts {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 2*time.Second)
		if err == nil {
			conn.Close()
			result.Ports = append(result.Ports, PortState{Port: port, State: "open", Name: portName(port)})
		}
	}
	return result, nil
}

// HTTPCheckResult HTTP 检测结果
type HTTPCheckResult struct {
	URL         string            `json:"url"`
	StatusCode  int               `json:"status_code"`
	TimeMs      int64             `json:"time_ms"`
	SizeBytes   int64             `json:"size_bytes"`
	RedirectURL string            `json:"redirect_url"`
	Headers     map[string]string `json:"headers"`
}

// HTTPCheck HTTP 请求检测
func HTTPCheck(url string) (*HTTPCheckResult, error) {
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "http://" + url
	}
	// SSRF 防护:拒绝回环/私网/链路本地目标,并用固定 IP 拨号防 DNS 重绑定
	if err := util.ValidatePublicURL(url); err != nil {
		return nil, err
	}
	client := util.PublicHTTPClient(10 * time.Second)
	start := time.Now()
	resp, err := client.Get(url)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return nil, fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	result := &HTTPCheckResult{
		URL:        url,
		StatusCode: resp.StatusCode,
		TimeMs:     elapsed,
		Headers:    map[string]string{},
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		result.RedirectURL = loc
	}
	for _, k := range []string{"Content-Type", "Server", "Content-Length", "X-Powered-By"} {
		if v := resp.Header.Get(k); v != "" {
			result.Headers[k] = v
		}
	}
	return result, nil
}

var portNames = map[int]string{
	21: "FTP", 22: "SSH", 23: "Telnet", 25: "SMTP", 53: "DNS", 80: "HTTP",
	110: "POP3", 143: "IMAP", 443: "HTTPS", 465: "SMTPS", 587: "SMTP-TLS",
	993: "IMAPS", 995: "POP3S", 1433: "MSSQL", 3306: "MySQL", 3389: "RDP",
	5432: "PostgreSQL", 6379: "Redis", 8080: "HTTP-Alt", 8443: "HTTPS-Alt",
	8888: "HTTP-Alt2", 9090: "Mgmt", 27017: "MongoDB",
}

func portName(port int) string {
	if n, ok := portNames[port]; ok {
		return n
	}
	return "Unknown"
}

// hostRe 主机名:必须以字母数字开头结尾,中间允许点与连字符。
// 首字符不允许 "-" 或 ".",杜绝把目标解析成命令行旗标(渗透测试发现:
// host="-V" 会触发 ping/traceroute 的版本旗标,进程以 root 运行)
var hostRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?$`)

// ipv6Re IPv6 字面量(含 ≥2 个冒号时整体校验)
var ipv6Re = regexp.MustCompile(`^([0-9a-fA-F]{0,4}:){1,7}[0-9a-fA-F]{0,4}$`)

func sanitizeHost(host string) string {
	host = strings.TrimSpace(host)
	if strings.Contains(host, "://") {
		host = host[strings.Index(host, "://")+3:]
	}
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	// IPv6 字面量(此前按 ":" 切端口会把地址截断,如 2606:4700::1111 -> 2606)
	if strings.Count(host, ":") >= 2 {
		if !ipv6Re.MatchString(host) {
			return ""
		}
		if _, err := netip.ParseAddr(host); err != nil {
			return ""
		}
		return host
	}
	// 单冒号视为 host:port,去掉端口
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	if !hostRe.MatchString(host) {
		return ""
	}
	return host
}
