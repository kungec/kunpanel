// filemgr/history.go —— 文件编辑历史版本:保存前自动快照,可列出/读取/恢复
package filemgr

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/util"
)

const (
	histKeep    = 20             // 每文件保留版本数
	histMaxByte = 8 << 20        // 单文件超 8MB 不留历史(编辑器本身也不加载这么大)
)

// HistItem 历史版本条目
type HistItem struct {
	TS   string `json:"ts"`   // 时间戳(文件名,unix)
	Size int64  `json:"size"` // 字节
	Time string `json:"time"` // 可读时间
}

func histDirFor(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	sum := sha1.Sum([]byte(abs))
	return filepath.Join(config.DataDir(), "file_history", hex.EncodeToString(sum[:12]))
}

// Snapshot 保存前把当前内容快照进历史(文件不存在=新文件,跳过;超限跳过;尽力而为)
func Snapshot(path string) {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() || fi.Size() > histMaxByte {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	dir := histDirFor(path)
	_ = os.MkdirAll(dir, 0o700)
	_ = util.AtomicWrite(filepath.Join(dir, fmt.Sprintf("%d.bak", time.Now().Unix())), data, 0o600)
	pruneHistory(dir)
}

// History 列出某文件的历史版本(新→旧)
func History(path string) []HistItem {
	dir := histDirFor(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []HistItem
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".bak") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		ts := strings.TrimSuffix(name, ".bak")
		out = append(out, HistItem{
			TS:   ts,
			Size: fi.Size(),
			Time: time.Unix(fi.ModTime().Unix(), 0).Format("2006-01-02 15:04:05"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS > out[j].TS })
	if len(out) > histKeep {
		out = out[:histKeep]
	}
	return out
}

// HistoryContent 读取指定版本内容
func HistoryContent(path, ts string) ([]byte, error) {
	if !validTS(ts) {
		return nil, fmt.Errorf("非法版本标识")
	}
	return os.ReadFile(filepath.Join(histDirFor(path), ts+".bak"))
}

// RestoreHistory 用历史版本覆盖当前文件(当前内容先快照,不丢改动)
func RestoreHistory(path, ts string) error {
	data, err := HistoryContent(path, ts)
	if err != nil {
		return err
	}
	Snapshot(path)
	return util.AtomicWrite(path, data, 0o644)
}

func validTS(ts string) bool {
	if len(ts) < 9 || len(ts) > 14 {
		return false
	}
	for _, c := range ts {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func pruneHistory(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var baks []os.DirEntry
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".bak") {
			baks = append(baks, e)
		}
	}
	if len(baks) <= histKeep {
		return
	}
	sort.Slice(baks, func(i, j int) bool { return baks[i].Name() > baks[j].Name() })
	for _, e := range baks[histKeep:] {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}
