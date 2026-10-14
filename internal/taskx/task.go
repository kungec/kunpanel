package taskx

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Task 后台长任务(如安装 Docker、部署应用),带滚动日志缓冲
type Task struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"` // running / success / failed
	Log       string    `json:"log"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`

	buf *ringBuffer
}

type ringBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.b = append(r.b, p...)
	if len(r.b) > r.max {
		r.b = r.b[len(r.b)-r.max:]
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.b)
}

type Manager struct {
	mu    sync.Mutex
	tasks map[string]*Task
	seq   int
}

var M = NewManager()

func NewManager() *Manager {
	return &Manager{tasks: map[string]*Task{}}
}

// Start 启动一个具名后台任务;同名任务运行中则拒绝
func (m *Manager) Start(name string, fn func(w io.Writer) error) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		if t.Name == name && t.Status == "running" {
			return nil, fmt.Errorf("任务[%s]正在执行中,请稍候", name)
		}
	}
	m.seq++
	t := &Task{
		ID:        fmt.Sprintf("t%d_%d", time.Now().Unix(), m.seq),
		Name:      name,
		Status:    "running",
		StartedAt: time.Now(),
		buf:       &ringBuffer{max: 512 * 1024},
	}
	m.tasks[t.ID] = t
	go func() {
		err := fn(t.buf)
		t.Finish(err)
	}()
	return t, nil
}

// Finish 标记任务结束
func (t *Task) Finish(err error) {
	if err != nil {
		t.Status = "failed"
		t.Error = err.Error()
	} else {
		t.Status = "success"
	}
	t.EndedAt = time.Now()
	t.Log = t.buf.String()
}

// Writer 返回任务日志写入器
func (t *Task) Writer() io.Writer { return t.buf }

func (m *Manager) Get(id string) (*Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if ok {
		t.Log = t.buf.String()
	}
	return t, ok
}

func (m *Manager) List() []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		t.Log = t.buf.String()
		out = append(out, t)
	}
	return out
}
