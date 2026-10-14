package websites

// antiparsing.go —— 防恶意解析:未绑定到任何站点的域名/IP 直接访问时断开连接,
// 防止他人把垃圾域名解析到本机 IP 分摊流量/蹭证书/染上搜索引擎劣迹。
import (
	"os"
	"path/filepath"
)

func antiParsingFlag() string { return filepath.Join(baseDir(), "anti_parsing") }

func AntiParsingEnabled() bool {
	_, err := os.Stat(antiParsingFlag())
	return err == nil
}

// ApplyAntiParsing 开启后 default_server 对未知 Host 返回 444(直接断开);
// 关闭则恢复默认欢迎页
func ApplyAntiParsing(enabled bool) error {
	if enabled {
		if err := os.WriteFile(antiParsingFlag(), []byte("1"), 0o644); err != nil {
			return err
		}
	} else {
		_ = os.Remove(antiParsingFlag())
	}
	conf := filepath.Join(baseDir(), "conf.d", "default.conf")
	var content string
	if enabled {
		content = "# 坤面板生成:防恶意解析(未知域名直接断开)\nserver {\n    listen 80 default_server;\n    server_name _;\n    return 444;\n}\n"
	} else {
		content = "# 坤面板生成:默认站点\nserver {\n    listen 80 default_server;\n    server_name _;\n    root /var/www/default;\n    index index.html;\n    location / { try_files $uri $uri/ =404; }\n}\n"
	}
	if err := os.WriteFile(conf, []byte(content), 0o644); err != nil {
		return err
	}
	return Reload()
}

// AntiParsingStatusToggle 切换并返回可能的错误信息(供 API 使用)
func AntiParsingStatusToggle(enabled bool) error {
	if enabled == AntiParsingEnabled() {
		return nil
	}
	return ApplyAntiParsing(enabled)
}

