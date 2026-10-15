package appstore

import (
	"net/http"
	"os/exec"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"kunpanel/internal/config"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/util"
)

// Param 安装参数定义
type Param struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Default  string   `json:"default"`
	Type     string   `json:"type"`              // text / port / password / select
	Options  []string `json:"options,omitempty"` // select 类型的可选项(镜像标签白名单)
	Help     string   `json:"help,omitempty"`
	Required bool     `json:"required"`
}

// Template 应用模板(内置)
type Template struct {
	Key         string                           `json:"key"`
	Name        string                           `json:"name"`
	Category    string                           `json:"category"`
	Desc        string                           `json:"desc"`
	Icon        string                           `json:"icon"` // SVG 图标名
	Version     string                           `json:"version"`
	Params      []Param                          `json:"params"`
	ComposeFrom func(p map[string]string) string `json:"-"`
	WebURL      func(p map[string]string) string `json:"-"`
	ComposeRaw  string                           `json:"compose,omitempty"` // 自定义应用的 compose 原文
}

// Instance 已安装的应用实例
type Instance struct {
	ID        string            `json:"id"`
	Key       string            `json:"key"`
	Name      string            `json:"name"`
	Params    map[string]string `json:"params"`
	Dir       string            `json:"dir"`
	CreatedAt string            `json:"created_at"`
	Status    string            `json:"status"` // 实时填充:running / exited / missing
}

// ---------- 内置应用目录 ----------

func Templates() []Template {
	return append(builtinTemplates(), CustomTemplates()...)
}

func builtinTemplates() []Template {
	return []Template{
		{
			Key: "nginx", Name: "Nginx", Category: "Web服务器", Icon: "globe", Version: "多版本可选",
			Desc: "全球第一的高性能 Web 服务器/反向代理(官方最新版,适配任意系统)",
			Params: []Param{
				{Key: "Version", Label: "Nginx 版本", Default: "latest", Type: "select", Required: true,
					Options: []string{"1.29", "1.28", "1.27", "1.26", "latest", "mainline-alpine", "stable-alpine", "alpine"},
					Help:    "1.29=最新主线版 · 1.28=最新稳定版 · 1.27/1.26=旧稳定版 · latest=最新稳定 · *-alpine=对应分支(精简)"},
				{Key: "HttpPort", Label: "HTTP 端口", Default: "80", Type: "port", Required: true},
				{Key: "HttpsPort", Label: "HTTPS 端口", Default: "443", Type: "port", Required: true},
			},
			ComposeFrom: func(p map[string]string) string {
				return fmt.Sprintf(`services:
  nginx:
    image: nginx:%s
    container_name: kun-nginx
    restart: always
    ports:
      - "%s:80"
      - "%s:443"
    volumes:
      - %s:/etc/nginx/conf.d
      - %s:/var/www
      - %s:/usr/local/kunwaf
      - %s:/etc/nginx/ssl
      - %s:/var/log/nginx
`, pval(p, "Version", "latest"), pval(p, "HttpPort", "80"), pval(p, "HttpsPort", "443"), confDir(), rootsDir(), pluginWafDir(), nginxSslDir(), nginxLogsDir())
			},
		},
		{
			Key: "openresty", Name: "OpenResty", Category: "Web服务器", Icon: "layout", Version: "多版本可选",
			Desc: "基于 Nginx 与 Lua 的高性能 Web 平台,面板网站功能依赖它",
			Params: []Param{
				{Key: "Version", Label: "OpenResty 版本", Default: "alpine", Type: "select", Required: true,
					Options: []string{"alpine", "latest", "jammy"},
					Help:    "alpine=最新稳定版(精简) · latest=最新完整版 · jammy=最新版(Ubuntu 基础)"},
				{Key: "HttpPort", Label: "HTTP 端口", Default: "80", Type: "port", Required: true},
				{Key: "HttpsPort", Label: "HTTPS 端口", Default: "443", Type: "port", Required: true},
			},
			ComposeFrom: func(p map[string]string) string {
				return fmt.Sprintf(`services:
  openresty:
    image: openresty/openresty:%s
    container_name: kun-openresty
    restart: always
    ports:
      - "%s:80"
      - "%s:443"
    volumes:
      - %s:/etc/nginx/conf.d
      - %s:/var/www
      - %s:/usr/local/kunwaf
      - %s:/etc/nginx/ssl
      - %s:/var/log/nginx
`, pval(p, "Version", "alpine"), pval(p, "HttpPort", "80"), pval(p, "HttpsPort", "443"), confDir(), rootsDir(), pluginWafDir(), nginxSslDir(), nginxLogsDir())
			},
		},
		{
			Key: "php", Name: "PHP 运行时", Category: "建站", Icon: "bolt", Version: "多版本可选",
			Desc: "官方 PHP-Apache 运行环境,上传 PHP 程序即可运行,可配合网站反向代理",
			Params: []Param{
				{Key: "Version", Label: "PHP 版本", Default: "8.4", Type: "select", Required: true,
					Options: []string{"8.1", "8.2", "8.3", "8.4", "8.5"},
					Help:    "8.5=最新版 · 8.4/8.3/8.2=稳定维护版 · 8.1=旧版兼容"},
				{Key: "Port", Label: "访问端口", Default: "8088", Type: "port", Required: true},
			},
			ComposeFrom: func(p map[string]string) string {
				return fmt.Sprintf(`services:
  php:
    image: php:%s-apache
    container_name: kun-php
    restart: always
    ports:
      - "172.17.0.1:%s:80"
    volumes:
      - %s:/var/www/html
`, pval(p, "Version", "8.4"), pval(p, "Port", "8088"), rootsDir())
			},
			WebURL: func(p map[string]string) string { return fmt.Sprintf("http://<IP>:%s", pval(p, "Port", "8088")) },
		},
		{
			Key: "mysql", Name: "MySQL", Category: "数据库", Icon: "database", Version: "多版本可选",
			Desc: "全球最流行的开源关系型数据库(官方全系列版本可选)",
			Params: []Param{
				{Key: "Version", Label: "MySQL 版本", Default: "8.4", Type: "select", Required: true,
					Options: []string{"9", "8.4", "8.0", "5.7", "5.6"},
					Help:    "9=最新创新版 · 8.4=最新 LTS 长期支持版 · 8.0=经典广泛兼容版 · 5.7/5.6=旧版兼容(仅 x86)"},
				{Key: "Port", Label: "端口", Default: "3306", Type: "port", Required: true},
				{Key: "RootPassword", Label: "root 密码", Default: "", Type: "password", Required: true},
			},
			ComposeFrom: func(p map[string]string) string {
				return fmt.Sprintf(`services:
  mysql:
    image: mysql:%s
    container_name: kun-mysql
    restart: always
    ports:
      - "127.0.0.1:%s:3306"
    environment:
      MYSQL_ROOT_PASSWORD: "%s"
    volumes:
      - ./data:/var/lib/mysql
`, pval(p, "Version", "8.4"), pval(p, "Port", "3306"), pval(p, "RootPassword", "Kun@123456"))
			},
		},
		{
			Key: "mariadb", Name: "MariaDB", Category: "数据库", Icon: "database", Version: "多版本可选",
			Desc: "MySQL 的社区分支,完全兼容(官方全系列版本可选)",
			Params: []Param{
				{Key: "Version", Label: "MariaDB 版本", Default: "11.8", Type: "select", Required: true,
					Options: []string{"10.11", "11.4", "11.8", "11"},
					Help:    "11=最新滚动版 · 11.8=最新 · 11.4=长期支持版 · 10.11=旧版兼容"},
				{Key: "Port", Label: "端口", Default: "3307", Type: "port", Required: true},
				{Key: "RootPassword", Label: "root 密码", Default: "", Type: "password", Required: true},
			},
			ComposeFrom: func(p map[string]string) string {
				return fmt.Sprintf(`services:
  mariadb:
    image: mariadb:%s
    container_name: kun-mariadb
    restart: always
    ports:
      - "127.0.0.1:%s:3306"
    environment:
      MARIADB_ROOT_PASSWORD: "%s"
    volumes:
      - ./data:/var/lib/mysql
`, pval(p, "Version", "11.8"), pval(p, "Port", "3307"), pval(p, "RootPassword", "Kun@123456"))
			},
		},
		{
			Key: "postgresql", Name: "PostgreSQL", Category: "数据库", Icon: "database", Version: "多版本可选",
			Desc: "功能强大的开源对象关系型数据库(官方全系列版本可选)",
			Params: []Param{
				{Key: "Version", Label: "PostgreSQL 版本", Default: "18", Type: "select", Required: true,
					Options: []string{"16", "17", "18"},
					Help:    "18=最新版 · 17=上一稳定版 · 16=广泛使用版"},
				{Key: "Port", Label: "端口", Default: "5432", Type: "port", Required: true},
				{Key: "Password", Label: "postgres 密码", Default: "", Type: "password", Required: true},
			},
			ComposeFrom: func(p map[string]string) string {
				return fmt.Sprintf(`services:
  postgres:
    image: postgres:%s-alpine
    container_name: kun-postgresql
    restart: always
    ports:
      - "127.0.0.1:%s:5432"
    environment:
      POSTGRES_PASSWORD: "%s"
    volumes:
      - ./data:/var/lib/postgresql/data
`, pval(p, "Version", "18"), pval(p, "Port", "5432"), pval(p, "Password", "Kun@123456"))
			},
		},
		{
			Key: "phpmyadmin", Name: "phpMyAdmin", Category: "数据库", Icon: "database", Version: "latest",
			Desc: "Web 端 MySQL/MariaDB 管理工具,可管理面板安装的数据库",
			Params: []Param{
				{Key: "Port", Label: "访问端口", Default: "8082", Type: "port", Required: true},
				{Key: "DbHost", Label: "默认数据库主机", Default: "kun-mysql", Type: "text",
					Help: "数据库容器名:MySQL 填 kun-mysql,MariaDB 填 kun-mariadb"},
			},
			ComposeFrom: func(p map[string]string) string {
				return fmt.Sprintf(`services:
  phpmyadmin:
    image: phpmyadmin:latest
    container_name: kun-phpmyadmin
    restart: always
    ports:
      - "%s:80"
    environment:
      PMA_HOST: "%s"
      PMA_ARBITRARY: "1"
`, pval(p, "Port", "8082"), pval(p, "DbHost", "kun-mysql"))
			},
			WebURL: func(p map[string]string) string { return fmt.Sprintf("http://<IP>:%s", pval(p, "Port", "8082")) },
		},
		{
			Key: "redis", Name: "Redis", Category: "数据库", Icon: "stack", Version: "多版本可选",
			Desc: "高性能内存键值数据库",
			Params: []Param{
				{Key: "Version", Label: "Redis 版本", Default: "8", Type: "select", Required: true,
					Options: []string{"7.2", "8"},
					Help:    "8=最新大版本 · 7.2=上一稳定版"},
				{Key: "Port", Label: "端口", Default: "6379", Type: "port", Required: true},
				{Key: "Password", Label: "访问密码", Default: "", Type: "password", Required: true},
			},
			ComposeFrom: func(p map[string]string) string {
				return fmt.Sprintf(`services:
  redis:
    image: redis:%s-alpine
    container_name: kun-redis
    restart: always
    ports:
      - "127.0.0.1:%s:6379"
    command: redis-server --requirepass "%s" --appendonly yes
    volumes:
      - ./data:/data
`, pval(p, "Version", "8"), pval(p, "Port", "6379"), pval(p, "Password", "Kun@123456"))
			},
		},
		{
			Key: "wordpress", Name: "WordPress", Category: "建站", Icon: "layout", Version: "latest",
			Desc: "全球最流行的博客/内容管理系统(自带 MySQL)",
			Params: []Param{
				{Key: "Port", Label: "访问端口", Default: "8080", Type: "port", Required: true},
				{Key: "DbPassword", Label: "数据库密码", Default: "", Type: "password", Required: true},
			},
			ComposeFrom: func(p map[string]string) string {
				return fmt.Sprintf(`services:
  wordpress:
    image: wordpress:latest
    container_name: kun-wordpress
    restart: always
    ports:
      - "%s:80"
    environment:
      WORDPRESS_DB_HOST: db
      WORDPRESS_DB_USER: wordpress
      WORDPRESS_DB_PASSWORD: "%s"
      WORDPRESS_DB_NAME: wordpress
    volumes:
      - ./wp:/var/www/html
    depends_on:
      - db
  db:
    image: mysql:8.0
    container_name: kun-wordpress-db
    restart: always
    environment:
      MYSQL_DATABASE: wordpress
      MYSQL_USER: wordpress
      MYSQL_PASSWORD: "%s"
      MYSQL_ROOT_PASSWORD: "%s"
    volumes:
      - ./db:/var/lib/mysql
`, pval(p, "Port", "8080"), pval(p, "DbPassword", "Kun@123456"), pval(p, "DbPassword", "Kun@123456"), pval(p, "DbPassword", "Kun@123456"))
			},
			WebURL: func(p map[string]string) string { return fmt.Sprintf("http://<IP>:%s", pval(p, "Port", "8080")) },
		},
		{
			Key: "portainer", Name: "Portainer", Category: "容器工具", Icon: "cube", Version: "ce",
			Desc: "图形化 Docker 容器管理平台",
			Params: []Param{
				{Key: "Port", Label: "访问端口", Default: "9000", Type: "port", Required: true},
			},
			ComposeFrom: func(p map[string]string) string {
				return fmt.Sprintf(`services:
  portainer:
    image: portainer/portainer-ce:latest
    container_name: kun-portainer
    restart: always
    ports:
      - "%s:9000"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./data:/data
`, pval(p, "Port", "9000"))
			},
			WebURL: func(p map[string]string) string { return fmt.Sprintf("http://<IP>:%s", pval(p, "Port", "9000")) },
		},
		{
			Key: "uptime-kuma", Name: "Uptime Kuma", Category: "运维监控", Icon: "pulse", Version: "1",
			Desc: "颜值超高的开源网站可用性监控",
			Params: []Param{
				{Key: "Port", Label: "访问端口", Default: "3001", Type: "port", Required: true},
			},
			ComposeFrom: func(p map[string]string) string {
				return fmt.Sprintf(`services:
  uptime-kuma:
    image: louislam/uptime-kuma:1
    container_name: kun-uptime-kuma
    restart: always
    ports:
      - "%s:3001"
    volumes:
      - ./data:/app/data
`, pval(p, "Port", "3001"))
			},
			WebURL: func(p map[string]string) string { return fmt.Sprintf("http://<IP>:%s", pval(p, "Port", "3001")) },
		},
		{
			Key: "memos", Name: "Memos", Category: "效率笔记", Icon: "feather", Version: "stable",
			Desc: "轻量级自托管备忘录/笔记",
			Params: []Param{
				{Key: "Port", Label: "访问端口", Default: "5230", Type: "port", Required: true},
			},
			ComposeFrom: func(p map[string]string) string {
				return fmt.Sprintf(`services:
  memos:
    image: neosmemo/memos:stable
    container_name: kun-memos
    restart: always
    ports:
      - "%s:5230"
    volumes:
      - ./data:/var/opt/memos
`, pval(p, "Port", "5230"))
			},
			WebURL: func(p map[string]string) string { return fmt.Sprintf("http://<IP>:%s", pval(p, "Port", "5230")) },
		},
		{
			Key: "it-tools", Name: "IT-Tools", Category: "效率工具", Icon: "tool", Version: "latest",
			Desc: "开发者常用小工具箱(加密/转换/生成器等)",
			Params: []Param{
				{Key: "Port", Label: "访问端口", Default: "8081", Type: "port", Required: true},
			},
			ComposeFrom: func(p map[string]string) string {
				return fmt.Sprintf(`services:
  it-tools:
    image: corentinth/it-tools:latest
    container_name: kun-it-tools
    restart: always
    ports:
      - "%s:80"
`, pval(p, "Port", "8081"))
			},
			WebURL: func(p map[string]string) string { return fmt.Sprintf("http://<IP>:%s", pval(p, "Port", "8081")) },
		},
        {
            Key: "kunfaka", Name: "坤发卡", Category: "建站", Icon: "store", Version: "latest",
            Desc: "全自动发卡系统:7×24 无人值守,6 大支付通道(含 USDT 免挂),秒级自动发货,内置应用商店+5 套主题",
            Params: []Param{
                {Key: "Port", Label: "监听端口", Default: "8080"},
                {Key: "DBRootPassword", Label: "MySQL root 密码", Default: "Kun@FK2026"},
            },
            ComposeFrom: func(p map[string]string) string {
                port := pval(p, "Port", "8080")
                return `services:
  kunfaka:
    image: kungec/kunfaka:latest
    container_name: kun-kunfaka
    restart: unless-stopped
    ports:
      - "` + port + `:80"
    environment:
      TZ: Asia/Shanghai
    volumes:
      - ./data:/var/www/html/data
      - ./uploads:/var/www/html/uploads
    depends_on:
      - db
  kunfaka-cron:
    image: kungec/kunfaka:latest
    container_name: kun-kunfaka-cron
    restart: unless-stopped
    command: ["sh", "-c", "while true; do php /var/www/html/cron.php; sleep 60; done"]
    volumes:
      - ./data:/var/www/html/data
      - ./uploads:/var/www/html/uploads
    depends_on:
      - db
  db:
    image: mysql:8.0
    container_name: kun-kunfaka-db
    restart: unless-stopped
    environment:
      MYSQL_DATABASE: kunfaka
      MYSQL_USER: kunfaka
      MYSQL_PASSWORD: Kun@FK2026
      MYSQL_ROOT_PASSWORD: ` + pval(p, "DBRootPassword", "Kun@FK2026") + `
      TZ: Asia/Shanghai
    volumes:
      - ./db_data:/var/lib/mysql
      - ./database.sql:/docker-entrypoint-initdb.d/01-init.sql:ro
`
            },
            WebURL: func(p map[string]string) string {
                return "http://<IP>:" + pval(p, "Port", "8080")
            },
        },
        {
            Key: "nat-cloud", Name: "NAT Cloud", Category: "建站", Icon: "cloud", Version: "latest",
            Desc: "NAT VPS 销售与自动化开通系统:套餐销售/LXC 节点全自动交付/计费/工单/实名/返利/开放 API",
            Params: []Param{
                {Key: "Port", Label: "监听端口", Default: "8082"},
                {Key: "DBRootPassword", Label: "MySQL root 密码", Default: "Kun@NC2026"},
            },
            ComposeFrom: func(p map[string]string) string {
                port := pval(p, "Port", "8082")
                return `services:
  web:
    image: php:8.2-apache
    container_name: natcloud-web
    ports:
      - "` + port + `:80"
    volumes:
      - ./app:/var/www/html/app
      - ./public:/var/www/html
      - ./cron:/var/www/html/cron
      - nc_data:/var/www/html/data
    depends_on:
      - db
    restart: unless-stopped
  db:
    image: mariadb:10.11
    container_name: natcloud-db
    environment:
      MYSQL_DATABASE: natcloud
      MYSQL_ROOT_PASSWORD: ` + pval(p, "DBRootPassword", "Kun@NC2026") + `
      TZ: Asia/Shanghai
    volumes:
      - nc_db:/var/lib/mysql
      - ./schema.sql:/docker-entrypoint-initdb.d/01-init.sql:ro
    restart: unless-stopped
    command: --character-set-server=utf8mb4 --collation-server=utf8mb4_general_ci
volumes:
  nc_data:
  nc_db:
`
            },
            WebURL: func(p map[string]string) string {
                return "http://<IP>:" + pval(p, "Port", "8082")
            },
        },
		{
			Key: "npm", Name: "Nginx Proxy Manager", Category: "Web服务器", Icon: "shuffle", Version: "latest",
			Desc: "可视化反向代理管理,自动申请 SSL 证书",
			Params: []Param{
				{Key: "Port", Label: "管理端口", Default: "81", Type: "port", Required: true},
			},
			ComposeFrom: func(p map[string]string) string {
				return fmt.Sprintf(`services:
  npm:
    image: jc21/nginx-proxy-manager:latest
    container_name: kun-npm
    restart: always
    ports:
      - "80:80"
      - "%s:81"
      - "443:443"
    volumes:
      - ./data:/data
      - ./letsencrypt:/etc/letsencrypt
`, pval(p, "Port", "81"))
			},
			WebURL: func(p map[string]string) string { return fmt.Sprintf("http://<IP>:%s", pval(p, "Port", "81")) },
		},
	}
}

func pval(p map[string]string, k, def string) string {
	if v := strings.TrimSpace(p[k]); v != "" {
		return v
	}
	return def
}

func dataRoot() string { return filepath.Join(config.DataDir(), "apps") }
func confDir() string  { return filepath.Join(config.DataDir(), "websites", "conf.d") }
func rootsDir() string { return filepath.Join(config.DataDir(), "websites", "roots") }

// pluginWafDir WAF 插件目录(挂载进 OpenResty 容器)
func pluginWafDir() string { return filepath.Join(config.DataDir(), "plugins", "waf") }

// nginxSslDir SSL 证书目录(挂载进 OpenResty/Nginx 容器,站点 conf 引用 /etc/nginx/ssl)
func nginxSslDir() string { return filepath.Join(config.DataDir(), "ssl") }

// nginxLogsDir 站点日志目录(挂载到容器 /var/log/nginx,持久化且可切割)
func nginxLogsDir() string { return filepath.Join(config.DataDir(), "websites", "logs") }

var storeMu sync.Mutex

func storeFile() string { return filepath.Join(dataRoot(), "instances.json") }

func loadInstances() []Instance {
	storeMu.Lock()
	defer storeMu.Unlock()
	var list []Instance
	if b, err := os.ReadFile(storeFile()); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	return list
}

func saveInstances(list []Instance) error {
	_ = os.MkdirAll(dataRoot(), 0o755)
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWrite(storeFile(), b, 0o600)
}

// GetTemplate 按 key 找模板
func GetTemplate(key string) (Template, bool) {
	for _, t := range Templates() {
		if t.Key == key {
			return t, true
		}
	}
	return Template{}, false
}

// List 已安装实例列表(带实时容器状态)
func List() ([]Instance, error) {
	list := loadInstances()
	cliOK, _ := dockerx.Installed()
	// 一次 docker ps 批量取全部容器状态(此前逐实例起子进程,引擎繁忙时列表被拖到秒级以上)
	var states map[string]bool
	if cliOK && len(list) > 0 {
		names := make([]string, len(list))
		for i := range list {
			names[i] = "kun-" + list[i].Key
		}
		states = dockerx.RunningStates(names...)
	}
	for i := range list {
		list[i].Status = "unknown"
		if !cliOK {
			continue
		}
		if states != nil && states["kun-"+list[i].Key] {
			list[i].Status = "running"
		} else {
			list[i].Status = "exited"
		}
	}
	sort.Slice(list, func(a, b int) bool { return list[a].CreatedAt > list[b].CreatedAt })
	return list, nil
}

// sanitizeParams 净化安装参数,阻断 docker-compose YAML 注入:
// - select 类型:仅允许模板预置的镜像标签白名单
// - 端口类型:仅保留数字
// - 其余:移除换行/制表/控制字符,转义反斜杠与双引号
func sanitizeParams(tpl Template, params map[string]string) map[string]string {
	out := make(map[string]string, len(params))
	for _, p := range tpl.Params {
		v := params[p.Key]
		switch p.Type {
		case "select":
			allowed := false
			for _, o := range p.Options {
				if v == o {
					allowed = true
					break
				}
			}
			// 动态版本白名单:Docker Hub 上真实存在的标签也可用
			if !allowed {
				for _, t := range HubTags(tpl.Key) {
					if v == t {
						allowed = true
						break
					}
				}
			}
			if !allowed {
				v = p.Default
			}
			out[p.Key] = v
			continue
		case "port":
			clean := strings.Map(func(r rune) rune {
				if r >= '0' && r <= '9' {
					return r
				}
				return -1
			}, v)
			if clean == "" {
				clean = p.Default
			}
			out[p.Key] = clean
			continue
		}
		v = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' || r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, v)
		v = strings.ReplaceAll(v, `\`, `\\`)
		v = strings.ReplaceAll(v, `"`, `\"`)
		out[p.Key] = v
	}
	return out
}

// SanitizeParamsPublic 供测试与外部调用的参数净化入口
func SanitizeParamsPublic(tpl Template, params map[string]string) map[string]string {
	return sanitizeParams(tpl, params)
}

// RenderComposePublic 供测试与外部调用的 compose 渲染入口
func RenderComposePublic(tpl Template, params map[string]string, w io.Writer) error {
	_, err := io.WriteString(w, tpl.ComposeFrom(sanitizeParams(tpl, params)))
	return err
}

// Install 渲染 compose 文件并登记实例(容器拉起由调用方在后台任务中执行 Apply)
func Install(key string, params map[string]string) error {
	tpl, ok := GetTemplate(key)
	if !ok {
		return fmt.Errorf("未知应用: %s", key)
	}
	for _, p := range tpl.Params {
		if p.Required && strings.TrimSpace(params[p.Key]) == "" && p.Default == "" {
			return fmt.Errorf("参数 %s 不能为空", p.Label)
		}
	}
	params = sanitizeParams(tpl, params)
	dir := filepath.Join(dataRoot(), key)
	_ = os.MkdirAll(dir, 0o755)

	// Nginx / OpenResty 首次安装时生成默认欢迎站点,避免 conf.d 为空无法访问
	if key == "nginx" || key == "openresty" {
		seedDefaultSite()
	}

	content := tpl.ComposeFrom(params)
	if err := util.AtomicWriteString(filepath.Join(dir, "docker-compose.yml"), content, 0o644); err != nil {
		return err
	}

	// 坤发卡:下载 database.sql 供 MySQL 首次启动自动导入
	if key == "kunfaka" {
		if err := installKunfaka(dir, params); err != nil {
			return fmt.Errorf("坤发卡初始化失败: %w", err)
		}
	}

	// NAT Cloud:下载源码和 schema.sql 供 MySQL 首次启动自动导入
	if key == "nat-cloud" {
		if err := installNatCloud(dir); err != nil {
			return fmt.Errorf("NAT Cloud 初始化失败: %w", err)
		}
	}

	list := loadInstances()
	for i := range list {
		if list[i].Key == key {
			list[i].Params = params
			return saveInstances(list)
		}
	}
	list = append(list, Instance{
		ID: fmt.Sprintf("%d", len(list)+1), Key: key, Name: tpl.Name,
		Params: params, Dir: dir, CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	})
	return saveInstances(list)
}

// installKunfaka 下载 kunfaka 源码与 database.sql 并写入实例目录
func installKunfaka(dir string, params map[string]string) error {
	const repo = "https://raw.githubusercontent.com/kungec/kunfaka/main"
	// 下载 database.sql(MySQL 首次启动自动导入)
	sqlURL := repo + "/database.sql"
	sqlData, err := downloadFile(sqlURL, 30*time.Second)
	if err != nil {
		return fmt.Errorf("下载 database.sql 失败: %w", err)
	}
	if err := util.AtomicWriteString(filepath.Join(dir, "database.sql"), string(sqlData), 0o644); err != nil {
		return err
	}
	return nil
}

// installNatCloud 下载 nat-cloud 源码包并解压到 compose 目录
func installNatCloud(dir string) error {
	tarURL := "https://github.com/kungec/nat-cloud/archive/refs/heads/main.tar.gz"
	data, err := downloadFile(tarURL, 60*time.Second)
	if err != nil {
		return fmt.Errorf("下载源码包失败: %w", err)
	}
	tmp := filepath.Join(dir, ".tmp_install")
	_ = os.RemoveAll(tmp)
	_ = os.MkdirAll(tmp, 0o755)
	tgz := filepath.Join(tmp, "src.tar.gz")
	if err := os.WriteFile(tgz, data, 0o644); err != nil {
		return err
	}
	cmd := exec.Command("tar", "-xzf", tgz, "-C", tmp, "--strip-components=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("解压失败: %v %s", err, strings.TrimSpace(string(out)))
	}
	// 复制源码目录和文件到 compose 目录
	for _, name := range []string{"app", "public", "cron", "install"} {
		srcDir := filepath.Join(tmp, name)
		dstDir := filepath.Join(dir, name)
		if _, err := os.Stat(srcDir); err == nil {
			filepath.WalkDir(srcDir, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(srcDir, p)
				dst := filepath.Join(dstDir, rel)
				if d.IsDir() {
					return os.MkdirAll(dst, 0o755)
				}
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				return os.WriteFile(dst, data, 0o644)
			})
		}
	}
	for _, f := range []string{"index.php", "router.php", ".htaccess", ".env.example"} {
		src := filepath.Join(tmp, f)
		if _, err := os.Stat(src); err == nil {
			data, _ := os.ReadFile(src)
			_ = util.AtomicWriteString(filepath.Join(dir, f), string(data), 0o644)
		}
	}
	// schema.sql 从 install/ 移到顶层
	if sb, err := os.ReadFile(filepath.Join(dir, "install", "schema.sql")); err == nil {
		_ = util.AtomicWrite(filepath.Join(dir, "schema.sql"), sb, 0o644)
	}
	_ = os.RemoveAll(tmp)
	return nil
}

// downloadFile 下载文件(带超时与大小限制)
func downloadFile(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

// InstanceDir 返回实例目录
func InstanceDir(key string) string {
	return filepath.Join(dataRoot(), key)
}

// GetCompose 读取实例的 docker-compose.yml 内容
func GetCompose(key string) (string, error) {
	if _, ok := GetTemplate(key); !ok {
		return "", fmt.Errorf("未知应用: %s", key)
	}
	b, err := os.ReadFile(filepath.Join(InstanceDir(key), "docker-compose.yml"))
	if err != nil {
		return "", fmt.Errorf("实例不存在或未安装")
	}
	return string(b), nil
}

// SaveCompose 校验并保存编辑后的 compose,然后重建容器使其生效
func SaveCompose(key, content string) error {
	if _, ok := GetTemplate(key); !ok {
		return fmt.Errorf("未知应用: %s", key)
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("内容不能为空")
	}
	if !strings.Contains(content, "services:") {
		return fmt.Errorf("这不是有效的 docker-compose 配置(缺少 services 段)")
	}
	dir := InstanceDir(key)
	tmp := filepath.Join(dir, "docker-compose.yml.new")
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	// 用 docker 自身校验 YAML 与配置合法性
	if out, err := dockerx.ComposeValidate(tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("配置校验失败: %s", tailStr(out))
	}
	if err := os.Rename(tmp, filepath.Join(dir, "docker-compose.yml")); err != nil {
		return err
	}
	if _, err := dockerx.Compose(dir, "up", "-d"); err != nil {
		return err
	}
	return nil
}

// PruneDanglingImages 清理悬空镜像
func PruneDanglingImages() (string, error) {
	return util.RunWithTimeout(10*time.Minute, "docker", "image", "prune", "-f")
}

// Apply 对实例执行 compose 动作(up -d / stop / restart / down)
func Apply(key, action string, w io.Writer) error {
	dir := InstanceDir(key)
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err != nil {
		return fmt.Errorf("实例不存在或未安装")
	}
	// 任务日志模式:输出实时流式写入,安装/拉取进度全程可见
	if w != nil {
		if strings.HasPrefix(action, "up") {
			fmt.Fprintln(w, "[1/2] 拉取镜像(首次可能需要数分钟)...")
			if err := dockerx.ComposeStream(dir, w, "pull"); err != nil {
				fmt.Fprintln(w, "镜像拉取出现问题(本地可能已有镜像),继续尝试启动 ...")
			}
			fmt.Fprintln(w, "[2/2] 创建并启动容器 ...")
		}
		if err := dockerx.ComposeStream(dir, w, strings.Fields(action)...); err != nil {
			fmt.Fprintln(w, "启动出现问题,尝试自动修复 compose 后重试 ...")
			if rerr := ReRenderCompose(key); rerr == nil {
				return dockerx.ComposeStream(dir, w, strings.Fields(action)...)
			}
			return fmt.Errorf("compose %s 失败", action)
		}
		fmt.Fprintln(w, "完成。")
		return nil
	}
	out, err := dockerx.Compose(dir, strings.Fields(action)...)
	if err != nil && isParseError(out) {
		if w != nil {
			fmt.Fprintln(w, "检测到 compose 文件损坏或为旧格式,自动修复后重试 ...")
		}
		if rerr := ReRenderCompose(key); rerr != nil {
			return fmt.Errorf("compose 文件损坏且自动修复失败: %v", rerr)
		}
		out, err = dockerx.Compose(dir, strings.Fields(action)...)
	}
	if w != nil {
		fmt.Fprintln(w, out)
	}
	if err != nil {
		return fmt.Errorf("compose %s 失败: %s", action, tailStr(out))
	}
	return nil
}

// isParseError 判断 compose 输出是否为 YAML 解析类错误
func isParseError(out string) bool {
	return strings.Contains(out, "failed to parse") || strings.Contains(out, "yaml:")
}

// ReRenderCompose 用当前模板 + 已保存参数重新生成 compose 文件(修复损坏/旧格式文件)
func ReRenderCompose(key string) error {
	tpl, ok := GetTemplate(key)
	if !ok {
		return fmt.Errorf("未知应用: %s", key)
	}
	list := loadInstances()
	var inst *Instance
	for i := range list {
		if list[i].Key == key {
			inst = &list[i]
			break
		}
	}
	if inst == nil {
		return fmt.Errorf("实例不存在")
	}
	dir := InstanceDir(key)
	_ = os.MkdirAll(dir, 0o755)
	content := tpl.ComposeFrom(sanitizeParams(tpl, inst.Params))
	return util.AtomicWriteString(filepath.Join(dir, "docker-compose.yml"), content, 0o644)
}

// forceRemoveContainers 兜底:compose 文件损坏无法 down 时,按名称前缀强删容器
func forceRemoveContainers(key string) {
	prefix := "kun-" + key
	ids, _ := util.RunWithTimeout(15*time.Second, "docker", "ps", "-aq", "--filter", "name=^/"+prefix)
	for _, id := range nonEmpty(ids) {
		_, _ = util.RunWithTimeout(20*time.Second, "docker", "rm", "-f", strings.TrimSpace(id))
	}
}

func nonEmpty(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Uninstall 卸载实例:down 容器,可选删除数据
func Uninstall(key string, removeData bool, w io.Writer) error {
	step := func(s string) {
		if w != nil {
			fmt.Fprintln(w, s)
		}
	}
	step("[1/2] 停止并移除容器 ...")
	dir := InstanceDir(key)
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err == nil {
		var buf strings.Builder
		err := dockerx.ComposeStream(dir, &buf, "down")
		if w != nil && buf.Len() > 0 {
			fmt.Fprintln(w, strings.TrimSpace(buf.String()))
		}
		if err != nil {
			if isParseError(buf.String()) {
				// 兼容损坏的 compose:先尝试修复重渲染,仍失败则强删容器
				step("compose 文件损坏,自动修复 ...")
				if rerr := ReRenderCompose(key); rerr == nil {
					var buf2 strings.Builder
					err = dockerx.ComposeStream(dir, &buf2, "down")
					if w != nil && buf2.Len() > 0 {
						fmt.Fprintln(w, strings.TrimSpace(buf2.String()))
					}
				}
			}
			if err != nil {
				step("回退为强制删除容器 ...")
				forceRemoveContainers(key)
			}
		}
	}
	step("[2/2] 更新应用记录 ...")
	list := loadInstances()
	var kept []Instance
	for _, it := range list {
		if it.Key != key {
			kept = append(kept, it)
		}
	}
	if err := saveInstances(kept); err != nil {
		return err
	}
	if removeData {
		step("[3/3] 删除应用数据目录 ...")
		_ = os.RemoveAll(dir)
	}
	step("卸载完成")
	return nil
}

func tailStr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return s[len(s)-400:]
	}
	return s
}

// seedDefaultSite conf.d 无任何站点时写入默认欢迎页配置
func seedDefaultSite() {
	entries, err := os.ReadDir(confDir())
	if err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".conf") {
				return // 已有站点配置,不动
			}
		}
	}
	_ = os.MkdirAll(confDir(), 0o755)
	_ = os.MkdirAll(filepath.Join(rootsDir(), "default"), 0o755)
	welcome := `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><title>坤面板 · Nginx 已就绪</title>
<style>body{font-family:-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;display:flex;align-items:center;justify-content:center;height:92vh;margin:0;background:#f2f4f7}
.box{text-align:center;background:#fff;padding:60px 80px;border-radius:16px;box-shadow:0 10px 40px rgba(0,60,160,.08)}
h1{font-size:22px;color:#303133}p{color:#909399;font-size:14px}</style></head>
<body><div class="box"><h1>Nginx 已由坤面板部署成功</h1>
<p>在【网站】页面创建站点,或通过【文件】管理 /opt/kunpanel/data/websites/roots 下的网页文件</p></div></body></html>`
	def := `server {
    listen 80 default_server;
    server_name _;
    root /var/www/default;
    index index.html;
    location / { try_files $uri $uri/ =404; }
}
`
	_ = os.WriteFile(filepath.Join(rootsDir(), "default", "index.html"), []byte(welcome), 0o644)
	_ = os.WriteFile(filepath.Join(confDir(), "default.conf"), []byte(def), 0o644)
}

// EnsureNginxMounts 确保面板管理的 nginx/openresty 容器挂载了 WAF 与 SSL 目录,
// 缺失时(旧版本安装的实例)自动补挂载并重建容器。供 WAF 插件与网站 SSL 模块调用。
func EnsureNginxMounts(key string, w io.Writer) error {
	if key != "openresty" && key != "nginx" {
		return nil
	}
	dir := InstanceDir(key)
	composeFile := filepath.Join(dir, "docker-compose.yml")
	b, err := os.ReadFile(composeFile)
	if err != nil {
		return nil // 未安装,忽略
	}
	content := string(b)
	changed := false
	for _, m := range []struct{ host, container string }{
		{filepath.Join(config.DataDir(), "plugins", "waf"), "/usr/local/kunwaf"},
		{nginxSslDir(), "/etc/nginx/ssl"},
		{filepath.Join(config.DataDir(), "websites", "auth"), "/etc/nginx/auth"},
		{nginxLogsDir(), "/var/log/nginx"},
	} {
		if strings.Contains(content, m.container) {
			continue
		}
		idx := strings.Index(content, "volumes:")
		if idx < 0 {
			return fmt.Errorf("%s compose 缺少 volumes 段,请卸载后重新安装", key)
		}
		lineEnd := strings.Index(content[idx:], "\n")
		insert := "\n      - " + m.host + ":" + m.container
		content = content[:idx+lineEnd] + insert + content[idx+lineEnd:]
		changed = true
	}
	if !changed {
		return nil
	}
	if err := util.AtomicWriteString(composeFile, content, 0o644); err != nil {
		return err
	}
	if w != nil {
		fmt.Fprintln(w, "  更新 compose 挂载,重建容器 ...")
	}
	out, err := dockerx.Compose(dir, "up", "-d")
	if w != nil {
		fmt.Fprintln(w, out)
	}
	if err != nil {
		return fmt.Errorf("重建容器失败: %s", tailStr(out))
	}
	for i := 0; i < 15; i++ {
		if dockerx.ContainerRunning("kun-" + key) {
			return nil
		}
		time.Sleep(time.Second)
	}
	return nil
}

// EnsurePhpMounts 确保 PHP 运行时挂载了站点根目录(创建 PHP 站点时调用)。
// 旧版 PHP 模板只挂载自身 www 目录,此函数自动补挂载并重建容器。
func EnsurePhpMounts(w io.Writer) error {
	dir := InstanceDir("php")
	composeFile := filepath.Join(dir, "docker-compose.yml")
	b, err := os.ReadFile(composeFile)
	if err != nil {
		return nil // 未安装,忽略
	}
	content := string(b)
	if strings.Contains(content, ":/var/www/html") && strings.Contains(content, "websites") {
		return nil
	}
	idx := strings.Index(content, "volumes:")
	if idx < 0 {
		return fmt.Errorf("PHP compose 缺少 volumes 段,请卸载后重新安装")
	}
	lineEnd := strings.Index(content[idx:], "\n")
	insert := "\n      - " + rootsDir() + ":/var/www/html"
	content = content[:idx+lineEnd] + insert + content[idx+lineEnd:]
	if err := util.AtomicWriteString(composeFile, content, 0o644); err != nil {
		return err
	}
	if w != nil {
		fmt.Fprintln(w, "  更新 PHP 挂载,重建容器 ...")
	}
	out, err := dockerx.Compose(dir, "up", "-d")
	if w != nil {
		fmt.Fprintln(w, out)
	}
	if err != nil {
		return fmt.Errorf("重建 PHP 容器失败: %s", tailStr(out))
	}
	for i := 0; i < 20; i++ {
		if dockerx.ContainerRunning("kun-php") {
			return nil
		}
		time.Sleep(time.Second)
	}
	return nil
}

// UpdateInstanceParam 更新已装实例的参数(如数据库修改 root 密码后同步记录)
func UpdateInstanceParam(key, param, value string) bool {
	list := loadInstances()
	found := false
	for i := range list {
		if list[i].Key == key {
			list[i].Params[param] = value
			found = true
		}
	}
	if !found {
		return false
	}
	return saveInstances(list) == nil
}
