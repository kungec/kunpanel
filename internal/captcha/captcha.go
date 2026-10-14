package captcha

import (
	"bytes"
	crand "crypto/rand"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Store 内存图形验证码:5 分钟有效、一次性使用
// 图形为 PNG 位图(字符经抖动/波浪扭曲+噪点干扰),不携带可机读文本
type Store struct {
	mu    sync.Mutex
	items map[string]item
	ttl   time.Duration
}

type item struct {
	answer string
	exp    time.Time
}

func NewStore() *Store {
	return &Store{items: map[string]item{}, ttl: 5 * time.Minute}
}

// 字符集去掉易混淆的 0O1lI
const charset = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"

// Freq 访问频率限制(防验证码接口被刷爆内存)
type Freq struct {
	mu     sync.Mutex
	hits   map[string]*freqWin
	limit  int
	window time.Duration
}

type freqWin struct {
	count int
	start time.Time
}

func NewFreq(limit int, window time.Duration) *Freq {
	return &Freq{hits: map[string]*freqWin{}, limit: limit, window: window}
}

// Allow 记录并判断是否放行
func (f *Freq) Allow(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	w, ok := f.hits[key]
	if !ok || now.Sub(w.start) > f.window {
		f.hits[key] = &freqWin{count: 1, start: now}
		// 顺带清理过期窗口
		for k, v := range f.hits {
			if now.Sub(v.start) > f.window {
				delete(f.hits, k)
			}
		}
		return true
	}
	w.count++
	return w.count <= f.limit
}

// New 生成一个新验证码,返回 id 与 base64 PNG
func (s *Store) New() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.items {
		if now.After(v.exp) {
			delete(s.items, k)
		}
	}

	code := make([]byte, 4)
	for i := range code {
		// 验证码字符必须用加密随机数:math/rand 状态可被观察输出推演,
		// 进而批量预测后续验证码(第三轮深度审计)
		code[i] = charset[cryptoInt(len(charset))]
	}
	id := randomID()
	s.items[id] = item{answer: string(code), exp: now.Add(s.ttl)}
	return id, renderPNG(string(code))
}

// Verify 校验并销毁(一次性)
func (s *Store) Verify(id, answer string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	if !ok {
		return false
	}
	delete(s.items, id)
	if time.Now().After(it.exp) {
		return false
	}
	return equalFoldASCII(strings.TrimSpace(answer), it.answer)
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func renderPNG(codeStr string) string {
	const (
		w, h = 150, 48
		amp    = 1.8
		period = 18.0
	)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{255, 255, 255, 255}
	drawFill(img, bg)

	// 极少干扰:1条线 + 少量噪点,全部在底层
	drawLine(img, randInt(0, w/3), randInt(4, h-4), randInt(w*2/3, w), randInt(4, h-4), color.RGBA{0, 102, 255, 80})

	// 大字体高对比绘制
	x := 10
	for _, ch := range codeStr {
		c := color.RGBA{20, 30, 60, 255} // 深色
		y := h/2 + 6 + randInt(-2, 2)
		drawChar(img, string(ch), x+randInt(-1, 1), y, c)
		x += 34
	}

	// 轻微波浪扭曲
	distort(img, amp, period)

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}


func drawFill(img *image.RGBA, c color.RGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.Set(x, y, c)
		}
	}
}

// drawChar 用 basicfont 点阵绘制单字符(不产生任何文本元数据)
func drawChar(img *image.RGBA, ch string, x, y int, c color.RGBA) {
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(ch)
}

func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	dx := abs(x1 - x0)
	dy := abs(y1 - y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx - dy
	for {
		img.Set(x0, y0, c)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x0 += sx
		}
		if e2 < dx {
			err += dx
			y0 += sy
		}
	}
}

// distort 波浪扭曲:每列按正弦垂直位移
func distort(img *image.RGBA, amp, period float64) {
	b := img.Bounds()
	w, h := b.Max.X, b.Max.Y
	phase := rand.Float64() * math.Pi * 2
	orig := image.NewRGBA(image.Rect(0, 0, w, h))
	copy(orig.Pix, img.Pix)
	for x := 0; x < w; x++ {
		shift := int(amp * math.Sin(float64(x)/period+phase))
		for y := 0; y < h; y++ {
			src := y - shift
			if src < 0 {
				src = 0
			}
			if src >= h {
				src = h - 1
			}
			img.Set(x, y, orig.At(x, src))
		}
	}
}


func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// randInt 仅用于噪线/字符抖动等装饰性绘制(非安全用途)
func randInt(min, max int) int {
	if max <= min {
		return min
	}
	return min + rand.Intn(max-min+1)
}

// cryptoInt 加密安全的 [0,max) 随机整数(拒绝采样避免模偏差)
func cryptoInt(max int) int {
	if max <= 0 {
		return 0
	}
	b := make([]byte, 1)
	for {
		_, _ = crand.Read(b)
		v := int(b[0])
		if lim := 256 - 256%max; v < lim {
			return v % max
		}
	}
}

func randomID() string {
	b := make([]byte, 12)
	_, _ = crand.Read(b)
	return fmt.Sprintf("%x", b)
}
