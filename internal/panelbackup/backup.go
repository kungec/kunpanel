package panelbackup

// panelbackup —— 面板配置打包(供下载接口与定时任务复用)
import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/util"
)

// Build 打包面板关键配置到临时文件,返回路径(调用方负责删除)
func Build() (string, error) {
	dir := config.DataDir()
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("kunpanel_backup_%d.tar.gz", time.Now().Unix()))

	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	files := map[string]string{
		"config.json": filepath.Join(dir, "config.json"),
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "ssl")); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				files["ssl/"+e.Name()] = filepath.Join(dir, "ssl", e.Name())
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "websites", "conf.d")); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				files["websites/conf.d/"+e.Name()] = filepath.Join(dir, "websites", "conf.d", e.Name())
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "apps")); err == nil {
		for _, e := range entries {
			cf := filepath.Join(dir, "apps", e.Name(), "docker-compose.yml")
			if util.Exists(cf) {
				files["apps/"+e.Name()+"/docker-compose.yml"] = cf
			}
		}
	}
	if util.Exists(filepath.Join(dir, "alert.json")) {
		files["alert.json"] = filepath.Join(dir, "alert.json")
	}

	for name, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data))}
		tw.WriteHeader(hdr)
		tw.Write(data)
	}
	tw.Close()
	gz.Close()
	f.Close()
	return tmp, nil
}
