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
	// 结构化进度(可选):任务体通过 P(w) 拿到 ProgressWriter 汇报,
	// 前端渲染进度条与当前阶段(如远程迁移到第几步)
	Progress *Progress `json:"progress,omitempty"`
	// 重放参数(可选):创建时由调用方附带,失败任务可在任务中心一键重试
	Retry *RetryData `json:"retry,omitempty"`

	buf *ringBuffer
	pw  *ProgressWriter
}

// RetryData 任务的重放参数:失败后在任务中心一键重试
type RetryData struct {
	Kind string         `json:"kind"` // 任务类型(重试分发用,如 acme-issue)
	Body map[string]any `json:"body"` // 原始请求参数
}

// TaskOption 任务创建可选项
type TaskOption func(*Task)

// WithRetry 附带重放参数(失败任务一键重试用)
func WithRetry(kind string, body map[string]any) TaskOption {
	return func(t *Task) {
		t.Retry = &RetryData{Kind: kind, Body: body}
	}
}

// Progress 任务进度快照
type Progress struct {
	Cur   int    `json:"cur"`
	Total int    `json:"total"`
	Stage string `json:"stage"`
}

// ProgressWriter 包装任务日志写入器,附带可原子汇报的进度
type ProgressWriter struct {
	io.Writer
	cur   int32
	total int32
	mu    sync.Mutex
	stage string
}

// P 从任务写入器取出进度汇报器(非任务写入器返回 nil)
func P(w io.Writer) *ProgressWriter {
	pw, _ := w.(*ProgressWriter)
	return pw
}

// Set 设置总步数并推进到第 cur 步,stage 为当前阶段文案
func (p *ProgressWriter) Set(cur, total int, stage string) {
	if p == nil {
		return
	}
	if cur < 0 {
		cur = 0
	}
	if total < 1 {
		total = 1
	}
	p.mu.Lock()
	p.cur, p.total, p.stage = int32(cur), int32(total), stage
	p.mu.Unlock()
}

// Step 仅推进步数(阶段文案不变)
func (p *ProgressWriter) Step(cur int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.cur = int32(cur)
	p.mu.Unlock()
}

// Stage 仅更新阶段文案
func (p *ProgressWriter) SetStage(stage string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.stage = stage
	p.mu.Unlock()
}

func (p *ProgressWriter) snapshot() Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Progress{Cur: int(p.cur), Total: int(p.total), Stage: p.stage}
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

// Start 启动一个具名后台任务;同名任务运行中则拒绝。
// 注意 sync.Mutex 不可重入:清理必须在拿锁之前完成(锁内调用 cleanupFinished
// 会自我死锁,曾导致首次卸载后整个任务系统永久挂死)。
func (m *Manager) Start(name string, fn func(w io.Writer) error, opts ...TaskOption) (*Task, error) {
	m.cleanupFinished() // 清理 1 小时前完成的任务(自己加锁)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		if t.Name == name && t.Status == "running" {
			return nil, fmt.Errorf("任务[%s]正在执行中,请稍候", name)
		}
	}
	m.seq++
	buf := &ringBuffer{max: 512 * 1024}
	pw := &ProgressWriter{Writer: buf}
	t := &Task{
		ID:        fmt.Sprintf("t%d_%d", time.Now().Unix(), m.seq),
		Name:      name,
		Status:    "running",
		StartedAt: time.Now(),
		buf:       buf,
		pw:        pw,
	}
	m.tasks[t.ID] = t
	for _, op := range opts {
		if op != nil {
			op(t)
		}
	}
	go func() {
		// 统一 panic 恢复:用户触发的任务(备份/清理/插件等)panic 不再拖垮整个面板
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(t.buf, "\n[任务异常崩溃] %v\n", r)
				m.finish(t, fmt.Errorf("任务 panic: %v", r))
			}
		}()
		err := fn(pw)
		m.finish(t, err)
	}()
	return t, nil
}

// Finish 标记任务结束(加锁,避免与 Get/List 并发读写竞态)
// bufString 快照读取滚动日志(nil 安全)
func (t *Task) bufString() string {
	if t.buf == nil {
		return t.Log
	}
	return t.buf.String()
}



// Writer 返回任务日志写入器
func (t *Task) Writer() io.Writer { return t.buf }

// finish 标记任务结束(锁 m.mu,与 Get/List 的字段读取互斥)
func (m *Manager) finish(t *Task, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		t.Status = "failed"
		t.Error = err.Error()
	} else {
		t.Status = "success"
	}
	t.EndedAt = time.Now()
	t.Log = t.bufString()
}

// cleanupFinished 清理 1 小时前完成的任务,防止 map 无限增长
func (m *Manager) cleanupFinished() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for id, t := range m.tasks {
		if t.Status != "running" && !t.EndedAt.IsZero() && now.Sub(t.EndedAt) > time.Hour {
			delete(m.tasks, id)
		}
	}
}

func (m *Manager) Get(id string) (*Task, bool) {
	m.mu.Lock()
	t, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return nil, false
	}
	t.Log = t.bufString()
	snap := *t
	snap.buf = nil
	if t.pw != nil {
		p := t.pw.snapshot()
		snap.Progress = &p
	}
	m.mu.Unlock()
	return &snap, true
}

func (m *Manager) List() []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		t.Log = t.bufString()
		snap := *t
		snap.buf = nil
		if t.pw != nil {
			p := t.pw.snapshot()
			snap.Progress = &p
		}
		out = append(out, &snap)
	}
	return out
}

// Remove 删除一条已结束的任务记录(running 任务不可删)
func (m *Manager) Remove(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || t.Status == "running" {
		return false
	}
	delete(m.tasks, id)
	return true
}

// ClearFailed 清空全部失败任务记录,返回清除条数
func (m *Manager) ClearFailed() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, t := range m.tasks {
		if t.Status == "failed" {
			delete(m.tasks, id)
			n++
		}
	}
	return n
}
