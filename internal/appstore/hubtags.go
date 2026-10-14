// appstore/hubtags.go —— 从 Docker Hub 动态拉取应用镜像的最新版本标签,
// 解决静态版本列表过期问题(如 nginx 最新主线版)。带 12 小时内存缓存。
package appstore

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var hubImages = map[string]string{
	"nginx":      "library/nginx",
	"php":        "library/php",
	"mysql":      "library/mysql",
	"mariadb":    "library/mariadb",
	"redis":      "library/redis",
	"postgresql": "library/postgres",
	"openresty":  "openresty/openresty",
}

// 各应用的标签筛选:只保留可用作版本的标签(nginx 要纯数字版本,php 要 -apache 后缀等)
var hubFilters = map[string]*regexp.Regexp{
	"nginx":      regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`),
	"php":        regexp.MustCompile(`^\d+\.\d+-apache$`),
	"mysql":      regexp.MustCompile(`^\d+(\.\d+)?$`),
	"mariadb":    regexp.MustCompile(`^\d+(\.\d+)?$`),
	"redis":      regexp.MustCompile(`^\d+(\.\d+)?$`),
	"postgresql": regexp.MustCompile(`^\d+(\.\d+)?(-alpine)?$`),
	"openresty":  regexp.MustCompile(`^\d+\.\d+\.\d+-alpine$`),
}

type hubTagEntry struct {
	tags []string
	at   time.Time
}

var hubTagStore sync.Map // image -> hubTagCache

// HubTags 返回该应用镜像的最新版本标签(新→旧,最多 8 个),失败返回 nil
func HubTags(key string) []string {
	image, ok := hubImages[key]
	if !ok {
		return nil
	}
	if c, ok := hubTagStore.Load(image); ok {
		if cc, ok := c.(hubTagEntry); ok && time.Since(cc.at) < 12*time.Hour {
			return cc.tags
		}
	}
	client := &http.Client{Timeout: 10 * time.Second}
	url := "https://hub.docker.com/v2/repositories/" + image + "/tags?page_size=100"
	var names []string
	seen := map[string]bool{}
	re := hubFilters[key]
	// 翻页拉取最多 3 页:热门镜像首页常被最新 RC 变体刷屏,稳定版标签在后面的页
	for page := 0; page < 3 && url != ""; page++ {
		resp, err := client.Get(url)
		if err != nil {
			break
		}
		var data struct {
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
			Next string `json:"next"`
		}
		err = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		if err != nil {
			break
		}
		for _, r := range data.Results {
			if !seen[r.Name] {
				seen[r.Name] = true
				names = append(names, r.Name)
			}
		}
		url = data.Next
	}
	var out []string
	for _, n := range names {
		if re != nil && !re.MatchString(n) {
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return hubVersionLess(out[j], out[i]) })
	if len(out) > 8 {
		out = out[:8]
	}
	hubTagStore.Store(image, hubTagEntry{tags: out, at: time.Now()})
	return out
}

// hubVersionLess 数值感知的版本比较:a 是否比 b 旧
func hubVersionLess(a, b string) bool {
	as, bs := hubSplitVer(a), hubSplitVer(b)
	for i := 0; i < 5; i++ {
		var av, bv int
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av != bv {
			return av < bv
		}
	}
	return a > b
}

func hubSplitVer(s string) []int {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' || r == '_' })
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		if n, err := strconv.Atoi(p); err == nil {
			out = append(out, n)
		}
	}
	return out
}
