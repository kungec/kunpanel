// renew.go —— 迁移站点的证书续期闭环。
//
// 此前迁移只是把宝塔证书文件拷进面板证书库:宝塔/acme.sh 后续续签的新证书
// 不会进入面板,迁移站点最长 90 天就会因证书过期打不开。
// 修复:流量切换(面板 Nginx 接管 80/443)成功后,自动为已导入的 SSL 站点
// 用面板自己的 ACME 引擎重新签发(HTTP 文件验证),签发成功即绑定并纳入
// 面板自动续期;失败(域名未解析到本机等)保留原宝塔证书并在日志说明。
package btmigrate

import (
	"fmt"
	"io"

	"kunpanel/internal/websites"
)

// RenewMigratedSites 为已导入且绑定证书的站点重新签发面板 ACME 证书。
// 在流量切换成功后调用(80 端口已由面板 Nginx 接管,HTTP 验证可达)。
// 已是面板 ACME 证书(Acme=true,自动续期)的站点自动跳过。
func RenewMigratedSites(w io.Writer) {
	st := loadState()
	if len(st.Imported) == 0 {
		return
	}
	fmt.Fprintln(w, ">> 为迁移站点接管证书续期(已是面板证书的自动跳过)...")
	renewed, skipped, failed := 0, 0, 0
	for _, s := range st.Imported {
		if !s.SSL || s.SiteID == "" {
			continue
		}
		site, err := websites.GetSite(s.SiteID)
		if err != nil || site.SslCertID == "" {
			continue
		}
		// 已是面板 ACME 签发(支持自动续期)则无需再签
		if certACME(site.SslCertID) {
			skipped++
			continue
		}
		fmt.Fprintf(w, "   %s:申请面板证书中(原宝塔证书继续生效,签发成功自动切换)...\n", s.Domain)
		if _, err := websites.IssueCertForSite(s.SiteID, websites.AcmeIssueOpts{Method: "http"}, w); err != nil {
			failed++
			fmt.Fprintf(w, "   %s:面板签发失败(%v),保留原宝塔证书;到期前请检查域名解析与本机 80 端口\n", s.Domain, err)
			continue
		}
		renewed++
		fmt.Fprintf(w, "   %s:已切换为面板证书,之后由面板自动续期\n", s.Domain)
	}
	fmt.Fprintf(w, ">> 证书续期接管完成:重签 %d,已是面板证书 %d,失败 %d\n", renewed, skipped, failed)
}

// certACME 查证书是否面板 ACME 签发
func certACME(certID string) bool {
	for _, c := range websites.ListCerts() {
		if c.ID == certID {
			return c.Acme
		}
	}
	return false
}
