// Package webstat 网站流量统计:解析 nginx access 日志,聚合 PV/UV/流量/Top 排行
package webstat

import (
	"bufio"
	"fmt"
	"kunpanel/internal/util"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Stats 聚合结果
type Stats struct {
	TotalPV    int            `json:"total_pv"`
	TotalUV    int            `json:"total_uv"`
	TotalBytes int64          `json:"total_bytes"`
	StatusDist map[string]int `json:"status_dist"` // 2xx/3xx/4xx/5xx/other
	TopIPs     []Rank         `json:"top_ips"`
	TopURIs    []Rank         `json:"top_uris"`
	Hourly     map[string]int `json:"hourly"` // "15:00" -> count
	Since      string         `json:"since"`
	SampleN    int            `json:"sample_n"`
}

// Rank 排行条目
type Rank struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
	Bytes int64  `json:"bytes"`
}

// accessLogRe nginx 默认 combined 格式
var accessLogRe = regexp.MustCompile(
	`^(\S+) \S+ \S+ \[([^\]]+)\] "([^"]*)" (\d{3}) (\d+|-)`)

// Analyze 解析日志内容并聚合
func Analyze(content string, maxLines int) *Stats {
	s := &Stats{
		StatusDist: map[string]int{},
		Hourly:     map[string]int{},
	}
	ipCount := map[string]int{}
	uriCount := map[string]int64{}
	uriBytes := map[string]int64{}
	uvSet := map[string]bool{}
	now := time.Now()
	lines := strings.Split(content, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	for _, line := range lines {
		m := accessLogRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ip := m[1]
		ts := m[2]
		request := m[3]
		status := m[4]
		bytesStr := m[5]

		s.TotalPV++
		s.SampleN++
		if !uvSet[ip] {
			uvSet[ip] = true
		}
		if bytesStr != "-" {
			if b, e := strconv.ParseInt(bytesStr, 10, 64); e == nil {
				s.TotalBytes += b
				uriParts := strings.SplitN(request, " ", 3)
				uri := "/"
				if len(uriParts) >= 2 {
					uri = uriParts[1]
				}
				uriCount[uri]++
				uriBytes[uri] += b
			}
		}
		// 状态码分类
		if len(status) == 3 {
			switch status[0] {
			case '2':
				s.StatusDist["2xx"]++
			case '3':
				s.StatusDist["3xx"]++
			case '4':
				s.StatusDist["4xx"]++
			case '5':
				s.StatusDist["5xx"]++
			default:
				s.StatusDist["other"]++
			}
		}
		// 小时桶(最近 24h)
		if t, e := time.Parse("02/Jan/2006:15:04:05 -0700", ts); e == nil {
			if now.Sub(t) < 24*time.Hour {
				hk := t.Format("15:04")
				s.Hourly[hk]++
			}
		}
		ipCount[ip]++
		uriTmp := strings.SplitN(request, " ", 3)
		uriKey := "/"
		if len(uriTmp) >= 2 {
			uriKey = strings.SplitN(uriTmp[1], "?", 2)[0]
		}
		_ = uriKey
	}
	s.TotalUV = len(uvSet)
	s.TopIPs = topNMap(ipCount, 10, nil)
	// URI 排行按次数
	type uriRank struct {
		uri   string
		count int64
		bytes int64
	}
	var uris []uriRank
	for u, c := range uriCount {
		uris = append(uris, uriRank{u, c, uriBytes[u]})
	}
	sort.Slice(uris, func(a, b int) bool { return uris[a].count > uris[b].count })
	for i, u := range uris {
		if i >= 10 {
			break
		}
		s.TopURIs = append(s.TopURIs, Rank{Key: u.uri, Count: int(u.count), Bytes: u.bytes})
	}
	return s
}

func topNMap(m map[string]int, n int, _ interface{}) []Rank {
	type kv struct {
		k string
		v int
	}
	var l []kv
	for k, v := range m {
		l = append(l, kv{k, v})
	}
	sort.Slice(l, func(a, b int) bool { return l[a].v > l[b].v })
	var out []Rank
	for i, e := range l {
		if i >= n {
			break
		}
		out = append(out, Rank{Key: e.k, Count: e.v})
	}
	return out
}

// GetLogContent 从容器或宿主机读取 access 日志尾部内容
func GetLogContent(container, logPath string, lines int) (string, error) {
	if container != "" {
		// 官方 openresty/nginx 镜像的 access.log 是指向 /dev/stdout 的符号链接,
		// docker exec tail 它等于 tail 自己的 stdout,会永久挂死。
		// 正确读取方式是 docker logs(日志驱动捕获的 stdout/stderr),并加超时兜底。
		return util.RunWithTimeout(15*time.Second, "docker", "logs", "--tail", fmt.Sprint(lines), container)
	}
	f, err := os.Open(logPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var content []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		content = append(content, sc.Text())
	}
	if len(content) > lines {
		content = content[len(content)-lines:]
	}
	return strings.Join(content, "\n"), nil
}

// LogPaths 根据 nginx 类型返回日志路径
func LogPaths(mode, target string) (container, accessLog string) {
	if mode == "container" {
		if target == "kun-openresty" {
			return target, "/usr/local/openresty/nginx/logs/access.log"
		}
		return target, "/var/log/nginx/access.log"
	}
	if runtime.GOOS == "linux" {
		return "", "/var/log/nginx/access.log"
	}
	return "", ""
}
