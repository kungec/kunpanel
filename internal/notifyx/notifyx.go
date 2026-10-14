package notifyx

// notifyx —— 面板内消息中心:告警/任务/安全事件在面板内留痕,
// 顶栏铃铛可查看未读。外发渠道(Webhook/邮件)不受影响。
import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/util"
)

const maxKeep = 200

type Item struct {
	ID    int64  `json:"id"`
	Time  string `json:"time"`
	Kind  string `json:"kind"` // alert / cron / login / cert / system
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

var (
	mu      sync.Mutex
	seq     int64
	lastSeq int64
)

func storePath() string { return filepath.Join(config.DataDir(), "notifications.json") }

// Add 写入一条消息(超过 200 条自动丢弃最旧的)
func Add(kind, title, body string) {
	mu.Lock()
	defer mu.Unlock()
	list := load()
	seq++
	if seq == 0 {
		seq = time.Now().UnixNano()
	}
	it := Item{
		ID:    seq,
		Time:  time.Now().Format("2006-01-02 15:04:05"),
		Kind:  kind,
		Title: title,
		Body:  body,
	}
	list = append(list, it)
	if len(list) > maxKeep {
		list = list[len(list)-maxKeep:]
	}
	_ = save(list)
}

// List 倒序返回最近 n 条
func List(n int) []Item {
	if n <= 0 || n > maxKeep {
		n = 50
	}
	mu.Lock()
	defer mu.Unlock()
	list := load()
	if len(list) > n {
		list = list[len(list)-n:]
	}
	out := make([]Item, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i])
	}
	return out
}

// Unread 统计 ID 大于 after 的未读数量
func Unread(after int64) int {
	mu.Lock()
	defer mu.Unlock()
	list := load()
	n := 0
	for _, it := range list {
		if it.ID > after {
			n++
		}
	}
	return n
}

// Clear 清空
func Clear() {
	mu.Lock()
	defer mu.Unlock()
	_ = save(nil)
}

func load() []Item {
	b, err := os.ReadFile(storePath())
	if err != nil {
		return nil
	}
	var list []Item
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	if len(list) > 0 {
		lastSeq = list[len(list)-1].ID
		if seq < lastSeq {
			seq = lastSeq
		}
	}
	return list
}

func save(list []Item) error {
	_ = os.MkdirAll(filepath.Dir(storePath()), 0o755)
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWrite(storePath(), b, 0o600)
}
