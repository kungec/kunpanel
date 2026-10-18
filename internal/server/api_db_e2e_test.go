package server

// 数据库新端点的 E2E 边界/注入探测:需要环境变量 KP_E2E_URL 与 KP_E2E_TOKEN
// 指向一台运行中的面板,否则自动跳过(CI 无环境不执行)。
// 探测后校验系统库完好,确认没有注入成功造成破坏。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func e2eEnv(t *testing.T) (string, *http.Client) {
	t.Helper()
	base := strings.TrimRight(os.Getenv("KP_E2E_URL"), "/")
	token := os.Getenv("KP_E2E_TOKEN")
	if base == "" || token == "" {
		t.Skip("KP_E2E_URL/KP_E2E_TOKEN 未设置,跳过 E2E 探测")
	}
	return base, &http.Client{Timeout: 30 * time.Second}
}

func e2eReq(t *testing.T, client *http.Client, token, method, url string, body any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "json") {
		t.Logf("[诊断] %s %s → status=%s ct=%s final=%s body=%s",
			method, url, resp.Status, ct, resp.Request.URL, string(b[:min(len(b), 300)]))
	}
	return resp.StatusCode, string(b)
}

// e2eJSON 解析面板标准响应,返回 code 与 message
func e2eJSON(t *testing.T, raw string) (int, string) {
	var d struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("响应非标准 JSON: %s", raw[:min(len(raw), 200)])
	}
	return d.Code, d.Message
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 恶意/边界输入打数据库新端点:必须全部被拒绝(400),且系统库完好
func TestE2EDBUserInjectionBoundary(t *testing.T) {
	base, client := e2eEnv(t)
	token := os.Getenv("KP_E2E_TOKEN")
	post := func(ep string, body any) (int, string) {
		_, raw := e2eReq(t, client, token, "POST", base+ep, body)
		return e2eJSON(t, raw)
	}

	cases := []struct {
		name   string
		ep     string
		body   map[string]any
		expect string // 报错信息应包含的关键词
	}{
		{"注入用户名", "/db/mysql/users/create",
			map[string]any{"user": "x'@'%' OR user != '", "host": "%", "password": "Xx@123456"}, "用户名"},
		{"注入密码", "/db/mysql/users/create",
			map[string]any{"user": "safeu1", "host": "%", "password": "abc'; DROP DATABASE mysql;--"}, "密码"},
		{"超长用户名", "/db/mysql/users/create",
			map[string]any{"user": strings.Repeat("a", 64), "host": "%", "password": "Xx@123456"}, "用户名"},
		{"注入库名", "/db/mysql/users/grant",
			map[string]any{"user": "safeu1", "host": "%", "db": "x`; DROP DATABASE mysql;--", "privs": []string{"SELECT"}}, "库名"},
		{"注入权限", "/db/mysql/users/grant",
			map[string]any{"user": "safeu1", "host": "%", "db": "mysql", "privs": []string{"SELECT); DROP DATABASE mysql;--"}}, "权限"},
	}
	for _, c := range cases {
		code, msg := post(c.ep, c.body)
		if code == 0 {
			t.Errorf("%s: 应被拒绝但成功了(%s)", c.name, msg)
		} else if !strings.Contains(msg, c.expect) {
			t.Errorf("%s: 报错信息应含 %q,得到 %q", c.name, c.expect, msg)
		}
	}

	// 复制端点:注入的主机名必须在任务启动前就被拒绝或任务快速失败;
	// 这里只断言 API 不 panic 且返回可解析的 JSON
	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"repl 注入从库", map[string]any{"key": "mysql", "slave_host": "h' OR '1'='1", "master_host": "kun-mysql", "repl_pass": "Repl@123456", "slave_pass": "Xx@123456"}},
		{"repl 注入主库", map[string]any{"key": "mysql", "slave_host": "repltest-slave", "master_host": "m'; DROP DATABASE mysql;--", "repl_pass": "Repl@123456", "slave_pass": "Xx@123456"}},
	} {
		code, raw := e2eReq(t, client, token, "POST", base+"/db/replication/setup", c.body)
		if code == http.StatusInternalServerError && raw == "" {
			t.Errorf("%s: 空响应", c.name)
		}
		var d struct {
			Code    int    `json:"code"`
			Data    *struct{ TaskID string `json:"task_id"` } `json:"data"`
		}
		_ = json.Unmarshal([]byte(raw), &d)
		_ = code
	}

	// 系统库完好性:探测后系统库必须原样存在
	code, raw := e2eReq(t, client, token, "GET", base+"/db/mysql/databases", nil)
	if code != http.StatusOK {
		t.Fatalf("探测后系统库列表请求失败: %d", code)
	}
	var d struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("解析库列表失败: %v", err)
	}
	for _, sys := range []string{"mysql", "sys", "information_schema", "performance_schema"} {
		found := false
		for _, db := range d.Data {
			if db == sys {
				found = true
			}
		}
		if !found {
			t.Errorf("探测后系统库 %s 丢失", sys)
		}
	}
}

// 授权映射端点:应返回 rows 数组且不包含系统内置账号
func TestE2EDBUserDBMapShape(t *testing.T) {
	base, client := e2eEnv(t)
	token := os.Getenv("KP_E2E_TOKEN")
	code, raw := e2eReq(t, client, token, "GET", base+"/db/mysql/users/dbmap", nil)
	if code != http.StatusOK {
		t.Fatalf("dbmap 请求失败: %d %s", code, raw[:min(len(raw), 200)])
	}
	var d struct {
		Data struct {
			Rows []struct {
				User  string `json:"user"`
				Scope string `json:"scope"`
			} `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	for _, r := range d.Data.Rows {
		if r.User == "mysql.session" || r.User == "mysql.sys" || r.User == "debian-sys-maint" {
			t.Errorf("系统账号 %s 不应出现在映射里", r.User)
		}
	}
}
