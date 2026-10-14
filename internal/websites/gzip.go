package websites

// gzip.go —— 全站 Gzip 压缩开关:注入 conf.d/kungzip.conf(html/css/js/json/svg/xml 等)
import (
	"os"
	"path/filepath"

	"kunpanel/internal/util"
)

func gzipFlag() string { return filepath.Join(baseDir(), "gzip_on") }
func gzipConf() string { return filepath.Join(confDir(), "kungzip.conf") }

const gzipContent = `# 坤面板生成:Gzip 压缩(全站)
gzip on;
gzip_comp_level 5;
gzip_min_length 1024;
gzip_vary on;
gzip_types text/plain text/css application/json application/javascript application/x-javascript text/xml application/xml application/xml+rss text/javascript image/svg+xml application/wasm font/ttf font/otf application/vnd.ms-fontobject;
`

func GzipEnabled() bool {
	_, err := os.Stat(gzipFlag())
	return err == nil
}

// ApplyGzip 开启/关闭全站 Gzip 并重载
func ApplyGzip(enabled bool) error {
	if enabled {
		if err := util.AtomicWrite(gzipFlag(), []byte("1"), 0o644); err != nil {
			return err
		}
		if err := util.AtomicWrite(gzipConf(), []byte(gzipContent), 0o644); err != nil {
			return err
		}
	} else {
		_ = os.Remove(gzipFlag())
		_ = os.Remove(gzipConf())
	}
	return Reload()
}
