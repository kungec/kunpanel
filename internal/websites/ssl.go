// websites/ssl.go —— SSL 证书管理:上传 PEM、一键自签、站点绑定
package websites

import (
	"math"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/util"
)

// CertInfo 证书元信息
type CertInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Domains string `json:"domains"`
	Expire  string `json:"expire"` // 2006-01-02
	Days    int    `json:"days"`   // 距到期天数,-1 表示永久(自签默认长期)
	SelfSigned bool `json:"self_signed"`
	Acme    bool   `json:"acme"`            // Let's Encrypt 签发,支持自动续期
	Staging bool   `json:"staging,omitempty"` // ACME 测试环境
	SiteID  string `json:"site_id,omitempty"` // 签发来源站点(续期时定位挑战目录)
	VMethod string `json:"v_method,omitempty"` // ACME 验证方式:http / dns
	VAlias  string `json:"v_alias,omitempty"`  // DNS 别名域名(CNAME 委托目标)
	Profile string `json:"profile,omitempty"`  // ACME 证书档案:shortlived = LE 短期证书(约 6 天;IP 证书仅此档案)
	Created string `json:"created"`
}

// renewDueDays 续期/临期判定阈值:普通证书 30 天;shortlived 短期证书按半衰期
// 约 3 天(6 天有效期,巡检 6h 一次足够及时,且远低于 LE 重复证书限速)
func (c CertInfo) renewDueDays() int {
	if c.Profile == "shortlived" {
		return 3
	}
	return 30
}

func sslDir() string { return filepath.Join(config.DataDir(), "ssl") }

// CertFilePath / KeyFilePath 站点证书与私钥文件路径(导出:面板 SSL 从站点
// 证书导入时读取文件内容)
func CertFilePath(id string) string { return certCrtPath(id) }
func KeyFilePath(id string) string  { return certKeyPath(id) }

func certMetaPath(id string) string  { return filepath.Join(sslDir(), id+".json") }
func certCrtPath(id string) string   { return filepath.Join(sslDir(), id+".crt") }
func certKeyPath(id string) string   { return filepath.Join(sslDir(), id+".key") }
func certCsrPath(id string) string   { return filepath.Join(sslDir(), id+".csr") }

// ListCerts 全部证书(按创建时间倒序)
func ListCerts() []CertInfo {
	entries, err := os.ReadDir(sslDir())
	if err != nil {
		return []CertInfo{}
	}
	var out []CertInfo
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(sslDir(), e.Name()))
		if err != nil {
			continue
		}
		var c CertInfo
		if json.Unmarshal(b, &c) == nil && c.ID != "" {
			out = append(out, c)
		}
	}
	// 倒序
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Created > out[i].Created {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// GetCert 按 ID 取证书元信息
func GetCert(id string) (CertInfo, bool) {
	for _, c := range ListCerts() {
		if c.ID == id {
			return c, true
		}
	}
	return CertInfo{}, false
}

// CertFilePaths 返回站点 conf 引用的容器内路径(挂载 DATA/ssl -> /etc/nginx/ssl)
func CertFilePaths(id string) (crt, key string, err error) {
	if _, ok := GetCert(id); !ok {
		return "", "", fmt.Errorf("证书不存在: %s", id)
	}
	return "/etc/nginx/ssl/" + id + ".crt", "/etc/nginx/ssl/" + id + ".key", nil
}

// UploadCert 上传 PEM 证书与私钥,校验后存储
func UploadCert(name, certPEM, keyPEM string) (CertInfo, error) {
	name = strings.TrimSpace(name)
	certPEM = strings.TrimSpace(certPEM)
	keyPEM = strings.TrimSpace(keyPEM)
	if name == "" || !domainTokenRe.MatchString(name) {
		return CertInfo{}, fmt.Errorf("证书名称仅支持字母数字与 . _ -")
	}
	if !strings.Contains(certPEM, "BEGIN CERTIFICATE") {
		return CertInfo{}, fmt.Errorf("证书内容无效(缺少 BEGIN CERTIFICATE)")
	}
	if !strings.Contains(keyPEM, "PRIVATE KEY") {
		return CertInfo{}, fmt.Errorf("私钥内容无效(缺少 PRIVATE KEY)")
	}
	id := "cert_" + fmt.Sprintf("%d", time.Now().UnixNano()/1000000)
	_ = os.MkdirAll(sslDir(), 0o755)
	if err := util.AtomicWrite(certCrtPath(id), []byte(certPEM+"\n"), 0o644); err != nil {
		return CertInfo{}, err
	}
	if err := util.AtomicWrite(certKeyPath(id), []byte(keyPEM+"\n"), 0o600); err != nil {
		return CertInfo{}, err
	}

	info := CertInfo{
		ID: id, Name: name, Domains: name,
		Created: time.Now().Format("2006-01-02 15:04:05"),
	}
	// openssl 读取域名与到期时间(尽力而为)
	if out, err := util.RunWithTimeout(10*time.Second, "openssl", "x509", "-in", certCrtPath(id), "-noout", "-subject", "-enddate"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "subject=") {
				if i := strings.Index(line, "CN = "); i >= 0 {
					info.Domains = strings.TrimSpace(strings.TrimPrefix(line[i:], "CN = "))
				} else if i := strings.Index(line, "CN="); i >= 0 {
					info.Domains = strings.TrimSpace(strings.TrimPrefix(line[i:], "CN="))
				}
			}
			if strings.HasPrefix(line, "notAfter=") {
				if t, e := time.Parse("Jan 2 15:04:05 2006 GMT", strings.TrimPrefix(line, "notAfter=")); e == nil {
					info.Expire = t.Format("2006-01-02")
					info.Days = int(time.Until(t).Hours() / 24)
				}
			}
		}
		info.SelfSigned = false
	}
	if err := saveCertMeta(info); err != nil {
		return CertInfo{}, err
	}
	return info, nil
}

// SelfSignCert 为域名生成自签证书
func SelfSignCert(domain string, days int) (CertInfo, error) {
	domain = strings.TrimSpace(strings.Fields(domain)[0])
	if !domainTokenRe.MatchString(domain) {
		return CertInfo{}, fmt.Errorf("域名包含非法字符")
	}
	if days <= 0 || days > 36500 {
		days = 3650
	}
	if _, err := exec.LookPath("openssl"); err != nil {
		return CertInfo{}, fmt.Errorf("服务器未安装 openssl,无法自签证书")
	}
	id := "cert_" + fmt.Sprintf("%d", time.Now().UnixNano()/1000000)
	_ = os.MkdirAll(sslDir(), 0o755)
	crt, key := certCrtPath(id), certKeyPath(id)
	out, err := util.RunWithTimeout(30*time.Second, "openssl", "req", "-x509", "-newkey", "rsa:2048", "-sha256", "-nodes",
		"-days", fmt.Sprintf("%d", days),
		"-subj", "/CN="+domain,
		"-addext", "subjectAltName=DNS:"+domain+",DNS:*."+domain,
		"-keyout", key, "-out", crt)
	if err != nil {
		return CertInfo{}, fmt.Errorf("openssl 失败: %s", strings.TrimSpace(out))
	}
	info := CertInfo{
		ID: id, Name: domain + "(自签)", Domains: domain,
		Expire: time.Now().AddDate(0, 0, days).Format("2006-01-02"),
		Days:   days, SelfSigned: true,
		Created: time.Now().Format("2006-01-02 15:04:05"),
	}
	if err := saveCertMeta(info); err != nil {
		return CertInfo{}, err
	}
	return info, nil
}

// DeleteCert 删除证书(被站点绑定时拒绝)
func DeleteCert(id string) error {
	mu.Lock()
	load()
	inUse := ""
	for _, s := range list {
		if s.SslCertID == id {
			inUse = s.Domain
			break
		}
	}
	mu.Unlock()
	if inUse != "" {
		return fmt.Errorf("证书正被站点 %s 使用,请先在该站点中解除 HTTPS", inUse)
	}
	if _, ok := GetCert(id); !ok {
		return fmt.Errorf("证书不存在")
	}
	_ = os.Remove(certMetaPath(id))
	_ = os.Remove(certCrtPath(id))
	_ = os.Remove(certKeyPath(id))
	return nil
}

func saveCertMeta(c CertInfo) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWrite(certMetaPath(c.ID), b, 0o600)
}

// EnsureSslDir 确保证书目录存在(挂载用)
func EnsureSslDir() {
	_ = os.MkdirAll(sslDir(), 0o755)
}

// StartCertWatch 证书到期巡检:每天一次 + 启动后 1 分钟首检,
// 剩余天数 ≤ warnDays 的证书通过 notify 回调推送(每天最多提醒一次)
func StartCertWatch(stop <-chan struct{}, notify func(msg string)) {
	go func() {
		time.Sleep(time.Minute)
		lastAlertDate := ""
		check := func() {
			for _, c := range ListCerts() {
				// 剩余天数按当前到期日实时计算(Days 仅签发时写入,不随时间流逝更新)
				days := c.Days
				if t, e := time.Parse("2006-01-02", c.Expire); e == nil {
					days = int(math.Ceil(time.Until(t).Hours() / 24))
				}
				if c.Expire == "" || days > 30 {
					continue
				}
				today := time.Now().Format("2006-01-02")
				if lastAlertDate == today {
					continue
				}
				lastAlertDate = today
				msg := fmt.Sprintf("【证书到期提醒】%s 将于 %s 到期(剩余 %d 天),请及时续期", c.Name, c.Expire, days)
				notify(msg)
			}
		}
		check()
		tick := time.NewTicker(24 * time.Hour)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				check()
			}
		}
	}()
}

// CertDownloadFiles 返回证书与私钥的宿主机文件路径(供下载打包)
func CertDownloadFiles(id string) (crt, key string, err error) {
	if _, ok := GetCert(id); !ok {
		return "", "", fmt.Errorf("证书不存在")
	}
	crt, key = certCrtPath(id), certKeyPath(id)
	if _, e := os.Stat(crt); e != nil {
		return "", "", fmt.Errorf("证书文件缺失")
	}
	if _, e := os.Stat(key); e != nil {
		return "", "", fmt.Errorf("私钥文件缺失")
	}
	return crt, key, nil
}
