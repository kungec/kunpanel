package appstore

// custom.go —— 自定义应用:用户提交任意 docker-compose 到应用商店,
// 与内置应用同一套 安装/实例/卸载 管理链路。
import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kunpanel/internal/dockerx"
	"kunpanel/internal/util"
)

func customTplPath() string { return filepath.Join(dataRoot(), "custom_templates.json") }

var customKeyRe = regexp.MustCompile(`^custom_[a-z0-9]+$`)

// CustomTemplates 读取自定义应用
func CustomTemplates() []Template {
	b, err := os.ReadFile(customTplPath())
	if err != nil {
		return nil
	}
	var out []Template
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	for i := range out {
		raw := out[i].ComposeRaw
		out[i].ComposeFrom = func(p map[string]string) string { return raw }
	}
	return out
}

func saveCustomTemplates(list []Template) error {
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(dataRoot(), 0o755)
	return util.AtomicWrite(customTplPath(), b, 0o600)
}

// AddCustomTemplate 新增自定义应用(校验 compose 可被 docker 解析)
func AddCustomTemplate(name, desc, compose string) (Template, error) {
	name = strings.TrimSpace(name)
	compose = strings.TrimSpace(compose)
	if name == "" {
		return Template{}, fmt.Errorf("应用名称不能为空")
	}
	if len(name) > 40 {
		return Template{}, fmt.Errorf("应用名称过长")
	}
	if !strings.Contains(compose, "services:") {
		return Template{}, fmt.Errorf("compose 缺少 services 段")
	}
	if len(compose) > 128*1024 {
		return Template{}, fmt.Errorf("compose 内容过大")
	}
	// 用 docker 自身校验合法性(非法 YAML/未知字段在安装时才会暴露,提前拦截)
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("kun_custom_%d.yml", time.Now().UnixNano()))
	if err := os.WriteFile(tmp, []byte(compose), 0o600); err != nil {
		return Template{}, err
	}
	defer os.Remove(tmp)
	if out, err := dockerx.ComposeValidate(tmp); err != nil {
		return Template{}, fmt.Errorf("compose 校验失败: %s", tailStr(out))
	}
	t := Template{
		Key:        "custom_" + fmt.Sprintf("%x", time.Now().UnixNano()),
		Name:       name,
		Category:   "自定义",
		Desc:       strings.TrimSpace(desc),
		Icon:       "puzzle",
		Version:    "自定义",
		ComposeRaw: compose,
	}
	list := CustomTemplates()
	list = append(list, t)
	if err := saveCustomTemplates(list); err != nil {
		return Template{}, err
	}
	return t, nil
}

// RemoveCustomTemplate 删除自定义应用(已安装的须先卸载)
func RemoveCustomTemplate(key string) error {
	if !customKeyRe.MatchString(key) {
		return fmt.Errorf("非法的自定义应用标识")
	}
	list := loadInstances()
	for _, inst := range list {
		if inst.Key == key {
			return fmt.Errorf("应用已安装,请先卸载再删除")
		}
	}
	tpls := CustomTemplates()
	out := tpls[:0]
	found := false
	for _, t := range tpls {
		if t.Key == key {
			found = true
			continue
		}
		out = append(out, t)
	}
	if !found {
		return fmt.Errorf("自定义应用不存在")
	}
	return saveCustomTemplates(out)
}
