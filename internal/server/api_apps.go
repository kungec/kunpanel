package server

import (
	"sync"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"kunpanel/internal/appstore"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/taskx"
	"kunpanel/internal/util"
	"kunpanel/internal/websites"
)

// handleAppTemplates 应用商店目录(前端渲染)
func (s *Server) handleAppTemplates(w http.ResponseWriter, r *http.Request) {
	list := appstore.Templates()
	if list == nil {
		list = []appstore.Template{}
	}
	OK(w, list)
}

func (s *Server) handleAppInstances(w http.ResponseWriter, r *http.Request) {
	list, err := appstore.List()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []appstore.Instance{}
	}
	OK(w, list)
}

type appInstallReq struct {
	Key    string            `json:"key"`
	Params map[string]string `json:"params"`
}

func (s *Server) handleAppInstall(w http.ResponseWriter, r *http.Request) {
	var req appInstallReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if _, ok := appstore.GetTemplate(req.Key); !ok {
		Fail(w, http.StatusBadRequest, "未知应用")
		return
	}
	if cliOK, _ := dockerx.Installed(); !cliOK {
		Fail(w, http.StatusBadRequest, "未检测到 Docker,请先在【容器】页面一键安装")
		return
	}
	// 安装/卸载/启停按应用键互斥:并发操作会导致记录与容器状态错乱
	unlock := lockAppOp(req.Key)
	defer unlock()
	// Install 返回实际实例键:PHP 多版本安装 "php" 会改写为 php82 等
	actualKey, err := appstore.Install(req.Key, req.Params)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	// 后台拉起容器并返回任务 ID(必须用实际键,否则会启动错误的实例)
	t, err := taskx.M.Start("app-"+actualKey, func(w io.Writer) error {
		return appstore.Apply(actualKey, "up -d", w)
	})
	if err != nil {
		Fail(w, http.StatusConflict, "已登记安装,但启动任务失败: "+err.Error())
		return
	}
	OK(w, t)
}

func (s *Server) handleAppAction(w http.ResponseWriter, r *http.Request) {
	key, ok := safeID(r.PathValue("key"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法应用标识")
		return
	}
	action := r.PathValue("action")
	var req struct {
		RemoveData bool `json:"remove_data"`
	}
	_ = readJSON(r, &req)
	// 与安装互斥,防卸载中重建/启停中删除记录等状态错乱
	unlock := lockAppOp(key)
	defer unlock()

	// 卸载涉及容器停止/移除/数据清理,放后台任务并流式输出进度
	if action == "uninstall" {
		// 卸载保护:PHP 多版本实例被站点使用时拒绝(避免站点瘫痪)
		if isPHPRuntimeKey(key) {
			if using := websites.PHPSitesUsing(key); len(using) > 0 {
				Fail(w, http.StatusConflict, fmt.Sprintf("有 %d 个站点正在使用该 PHP 版本(%s),请先在站点设置中切换版本后再卸载", len(using), strings.Join(using, ", ")))
				return
			}
		}
		t, err := taskx.M.Start("app-"+key+"-uninstall", func(w io.Writer) error {
			return appstore.Uninstall(key, req.RemoveData, w)
		})
		if err != nil {
			Fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		OK(w, map[string]string{"task_id": t.ID})
		return
	}
	// start/stop/restart 通常很快,直接同步执行
	switch action {
	case "start":
		// 容器已被删除(如手动 docker rm、异常清理)时,启动自动转为重建:
		// compose 与数据目录仍在,up -d 后数据不丢
		act := "start"
		if !dockerx.ContainerExists("kun-" + key) {
			act = "up -d"
		}
		err := appstore.Apply(key, act, nil)
		if err != nil {
			Fail(w, http.StatusInternalServerError, err.Error())
			return
		}
	case "stop":
		err := appstore.Apply(key, "stop", nil)
		if err != nil {
			Fail(w, http.StatusInternalServerError, err.Error())
			return
		}
	case "restart":
		err := appstore.Apply(key, "restart", nil)
		if err != nil {
			Fail(w, http.StatusInternalServerError, err.Error())
			return
		}
	case "up":
		// 按当前 compose 重建容器(编辑配置后使其生效;restart 不会应用新配置)
		err := appstore.Apply(key, "up -d", nil)
		if err != nil {
			Fail(w, http.StatusInternalServerError, err.Error())
			return
		}
	default:
		Fail(w, http.StatusBadRequest, "不支持的操作: "+action)
		return
	}
	OK(w, nil)
}

// handleAppHubTags 动态获取应用镜像的最新版本标签(Docker Hub)
func (s *Server) handleAppHubTags(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	tags := appstore.HubTags(key)
	if tags == nil {
		OK(w, []string{})
		return
	}
	OK(w, tags)
}

// ---------- 应用运行时管理(PHP / Nginx / OpenResty) ----------

var runtimeContainers = map[string]string{
	"php": "kun-php", "nginx": "kun-nginx", "openresty": "kun-openresty",
	"mysql": "kun-mysql", "mariadb": "kun-mariadb", "postgresql": "kun-postgresql", "redis": "kun-redis",
}

func runtimeContainerOf(key string) (string, bool) {
	// PHP 多版本实例:php81/php82... → kun-php81/kun-php82...
	if isPHPRuntimeKey(key) {
		return "kun-" + key, true
	}
	c, ok := runtimeContainers[key]
	return c, ok
}

// appOpsMu 应用操作按键互斥(安装/卸载/启停串行化,防记录与容器状态竞态)
var (
	appOpsMu   sync.Mutex
	appOpLocks = map[string]*sync.Mutex{}
)

func lockAppOp(key string) func() {
	appOpsMu.Lock()
	defer appOpsMu.Unlock()
	mu, ok := appOpLocks[key]
	if !ok {
		mu = &sync.Mutex{}
		appOpLocks[key] = mu
	}
	mu.Lock()
	return mu.Unlock
}

// isPHPRuntimeKey php 或 php+版本数字(排除 phpmyadmin)
func isPHPRuntimeKey(key string) bool {
	return appstore.IsPHPRuntimeKey(key)
}

// handleAppRuntimeInfo 运行时信息:版本号(PHP/php-fpm/nginx/openresty -v)、PHP 已加载扩展
func (s *Server) handleAppRuntimeInfo(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	container, ok := runtimeContainerOf(key)
	if !ok {
		Fail(w, http.StatusBadRequest, "该应用不支持运行时管理")
		return
	}
	if !dockerx.ContainerRunning(container) {
		Fail(w, http.StatusBadRequest, "应用未运行")
		return
	}
	out := map[string]any{"key": key}
	// PHP 多版本实例(php/php82/…)共用同一套运行时信息采集,按实例的容器执行
	if isPHPRuntimeKey(key) {
		if v, err := dockerx.ExecIn(container, "php", "-v"); err == nil {
			out["version"] = strings.TrimSpace(strings.SplitN(v, "\n", 2)[0])
		}
		if m, err := dockerx.ExecIn(container, "php", "-m"); err == nil {
			var exts []string
			for _, line := range strings.Split(m, "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "[") {
					exts = append(exts, line)
				}
			}
			out["extensions"] = exts
		}
		// 可卸载扩展:有独立 conf.d/docker-php-ext-*.ini 开关文件的才算(内置模块无此文件)
		if ls, err := dockerx.ExecIn(container, "sh", "-c",
			"ls /usr/local/etc/php/conf.d/ 2>/dev/null | grep '^docker-php-ext-' | sed 's/^docker-php-ext-//;s/\\.ini$//'"); err == nil {
			var rem []string
			for _, line := range strings.Split(ls, "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					rem = append(rem, line)
				}
			}
			out["removable"] = rem
		}
		// 常用 ini 设置(读取 zz-kunpanel.ini 覆盖文件,不存在则读默认值)
		setRaw, _ := dockerx.ExecIn(container, "sh", "-c", "cat /usr/local/etc/php/conf.d/zz-kunpanel.ini 2>/dev/null || echo ''")
		settings := map[string]string{}
		for _, k := range []string{"upload_max_filesize", "post_max_size", "max_execution_time", "memory_limit", "display_errors"} {
			settings[k] = ""
		}
		for _, line := range strings.Split(setRaw, "\n") {
			kv := strings.SplitN(line, "=", 2)
			if len(kv) == 2 {
				k := strings.TrimSpace(kv[0])
				if _, ok := settings[k]; ok {
					settings[k] = strings.TrimSpace(kv[1])
				}
			}
		}
		out["settings"] = settings
	} else if key == "mysql" || key == "mariadb" {
		bin := "mysqld"
		if key == "mariadb" {
			bin = "mariadbd"
		}
		if v, err := dockerx.ExecIn(container, "sh", "-c", bin+" --version || mysqld --version"); err == nil {
			out["version"] = strings.TrimSpace(strings.SplitN(v, "\n", 2)[0])
		}
	} else if key == "postgresql" {
		if v, err := dockerx.ExecIn(container, "psql", "--version"); err == nil {
			out["version"] = strings.TrimSpace(strings.SplitN(v, "\n", 2)[0])
		}
	} else if key == "redis" {
		if v, err := dockerx.ExecIn(container, "redis-server", "--version"); err == nil {
			out["version"] = strings.TrimSpace(strings.SplitN(v, "\n", 2)[0])
		}
	} else {
		sub := "-v"
		if key == "openresty" {
			sub = "-V"
		}
		if v, err := dockerx.ExecIn(container, "nginx", sub); err == nil {
			out["version"] = strings.TrimSpace(strings.SplitN(v, "\n", 2)[0])
		}
	}
	OK(w, out)
}

// handleAppRuntimeAction 运行时操作:reload(重载配置)/ test(配置测试)/ restart / php-ext(安装扩展) / php-ini(写入设置)
func (s *Server) handleAppRuntimeAction(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	action := r.PathValue("action")
	if action == "" {
		action = r.URL.Query().Get("action")
	}
	container, ok := runtimeContainerOf(key)
	if !ok {
		Fail(w, http.StatusBadRequest, "该应用不支持运行时管理")
		return
	}
	if !dockerx.ContainerRunning(container) {
		Fail(w, http.StatusBadRequest, "应用未运行")
		return
	}
	var req struct {
		Ext      string            `json:"ext"`
		Content  string            `json:"content"`
		Settings map[string]string `json:"settings"`
	}
	_ = readJSON(r, &req)

	switch action {
	case "test":
		out, err := dockerx.ExecIn(container, "nginx", "-t")
		if err != nil || strings.Contains(out, "failed") {
			Fail(w, http.StatusBadRequest, strings.TrimSpace(out))
			return
		}
		OK(w, strings.TrimSpace(out))
	case "reload":
		if out, err := dockerx.ExecIn(container, "nginx", "-t"); err != nil || strings.Contains(out, "failed") {
			Fail(w, http.StatusBadRequest, "配置校验失败: "+strings.TrimSpace(out))
			return
		}
		if _, err := dockerx.ExecIn(container, "nginx", "-s", "reload"); err != nil {
			Fail(w, http.StatusInternalServerError, "重载失败")
			return
		}
		OK(w, "已重载")
	case "php-ext":
		if !isPHPRuntimeKey(key) {
			Fail(w, http.StatusBadRequest, "仅 PHP 支持扩展安装")
			return
		}
		ext := strings.TrimSpace(req.Ext)
		if !regexp.MustCompile(`^[a-z0-9_]{2,30}$`).MatchString(ext) {
			Fail(w, http.StatusBadRequest, "扩展名格式不正确")
			return
		}
		meta, ok := phpExtCatalog()[ext]
		if !ok {
			Fail(w, http.StatusBadRequest, "暂不支持该扩展: "+ext)
			return
		}
		// 已加载守卫:避免重复安装产生混乱状态(opcache 在 PHP 8.4 php -m 显示为 "Zend OPcache")
		if mout, err := dockerx.ExecIn(container, "php", "-m"); err == nil {
			for _, line := range strings.Split(mout, "\n") {
				norm := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(line), " ", ""))
				if norm == ext || norm == "zend"+ext {
					Fail(w, http.StatusConflict, "扩展 "+ext+" 已安装并加载,无需重复安装")
					return
				}
			}
		}
		t, err := taskx.M.Start("php-ext-"+ext, func(w io.Writer) error {
			fmt.Fprintln(w, "安装 PHP 扩展:", ext, "("+map[bool]string{true: "PECL", false: "源码内置"}[meta.Pecl]+")")
			// 1) 系统依赖(镜像默认不含图形/压缩等开发库)
			if strings.TrimSpace(meta.Deps) != "" {
				fmt.Fprintln(w, "[1/3] 安装系统依赖: "+meta.Deps)
				if err := util.RunWithTimeoutWriter(5*time.Minute, w, "docker", "exec", container, "sh", "-c",
					"apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "+meta.Deps); err != nil {
					return fmt.Errorf("系统依赖安装失败(请检查扩展是否适配当前系统): %v", err)
				}
			}
			// 2) 编译安装
			if meta.Pecl {
				fmt.Fprintln(w, "[2/3] pecl install " + ext + " (编译约 2~5 分钟)...")
				if err := util.RunWithTimeoutWriter(12*time.Minute, w, "docker", "exec", container, "pecl", "install", ext); err != nil {
					return fmt.Errorf("pecl 安装失败: %v", err)
				}
				if _, e := dockerx.ExecIn(container, "docker-php-ext-enable", ext); e != nil {
					return fmt.Errorf("启用扩展失败: %v", e)
				}
			} else {
				if strings.TrimSpace(meta.Conf) != "" {
					fmt.Fprintln(w, "[2/3] 配置编译参数: "+meta.Conf)
					confArgs := append([]string{"exec", container, "docker-php-ext-configure", ext}, strings.Fields(meta.Conf)...)
					if err := util.RunWithTimeoutWriter(5*time.Minute, w, "docker", confArgs...); err != nil {
						return fmt.Errorf("编译配置失败: %v", err)
					}
				}
				fmt.Fprintln(w, "[2/3] 源码编译 " + ext + " (约 1~5 分钟)...")
				if err := util.RunWithTimeoutWriter(12*time.Minute, w, "docker", "exec", container, "docker-php-ext-install", ext); err != nil {
					return fmt.Errorf("编译安装失败: %v", err)
				}
			}
			// 3) 重启加载
			fmt.Fprintln(w, "[3/3] 重启容器加载扩展 ...")
			return util.RunWithTimeoutWriter(60*time.Second, w, "docker", "restart", container)
		})
		if err != nil {
			Fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		OK(w, map[string]string{"task_id": t.ID})
	case "php-ext-remove":
		if !isPHPRuntimeKey(key) {
			Fail(w, http.StatusBadRequest, "仅 PHP 支持扩展卸载")
			return
		}
		ext := strings.TrimSpace(req.Ext)
		if !regexp.MustCompile(`^[a-z0-9_]{2,30}$`).MatchString(ext) {
			Fail(w, http.StatusBadRequest, "扩展名格式不正确")
			return
		}
		t, err := taskx.M.Start("php-ext-remove-"+ext, func(w io.Writer) error {
			fmt.Fprintln(w, "卸载 PHP 扩展:", ext)
			// 1) 必须存在 docker-php-ext 开关文件,否则是内置模块,不可卸载
			if _, e := dockerx.ExecIn(container, "sh", "-c",
				"test -f /usr/local/etc/php/conf.d/docker-php-ext-"+ext+".ini"); e != nil {
				return fmt.Errorf("该扩展是 PHP 内置模块(或未通过面板安装),无法卸载")
			}
			fmt.Fprintln(w, "[1/3] 移除加载开关与扩展文件 ...")
			script := "rm -f /usr/local/etc/php/conf.d/docker-php-ext-" + ext + ".ini && " +
				"SO=$(php -r 'echo ini_get(\"extension_dir\");') && rm -f \"$SO/" + ext + ".so\"; true"
			if _, e := dockerx.ExecIn(container, "sh", "-c", script); e != nil {
				return fmt.Errorf("移除扩展文件失败: %v", e)
			}
			fmt.Fprintln(w, "[2/3] 重启容器 ...")
			if err := util.RunWithTimeoutWriter(60*time.Second, w, "docker", "restart", container); err != nil {
				return err
			}
			// 3) 复核确实不再加载
			fmt.Fprintln(w, "[3/3] 校验卸载结果 ...")
			m, _ := dockerx.ExecIn(container, "php", "-m")
			for _, line := range strings.Split(m, "\n") {
				norm := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(line), " ", ""))
				if norm == ext || norm == "zend"+ext {
					return fmt.Errorf("卸载后仍检测到 %s(可能为内置模块),请勿强行移除", ext)
				}
			}
			fmt.Fprintln(w, "✅ 扩展 "+ext+" 已卸载")
			return nil
		})
		if err != nil {
			Fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		OK(w, map[string]string{"task_id": t.ID})
	case "php-ext-catalog":
		if !isPHPRuntimeKey(key) {
			Fail(w, http.StatusBadRequest, "仅 PHP 支持扩展安装")
			return
		}
		OK(w, phpExtCatalog())
	case "php-ini-get":
		if !isPHPRuntimeKey(key) {
			Fail(w, http.StatusBadRequest, "仅 PHP 支持配置编辑")
			return
		}
		// php.ini 不存在时(官方镜像默认不带)以 production 模板初始化
		if _, err := dockerx.ExecIn(container, "sh", "-c", "test -f /usr/local/etc/php/php.ini"); err != nil {
			if _, e := dockerx.ExecIn(container, "sh", "-c",
				"test -f /usr/local/etc/php/php.ini-production && cp /usr/local/etc/php/php.ini-production /usr/local/etc/php/php.ini || touch /usr/local/etc/php/php.ini"); e != nil {
				Fail(w, http.StatusInternalServerError, "初始化 php.ini 失败: "+e.Error())
				return
			}
		}
		b64, err := dockerx.ExecIn(container, "sh", "-c", "base64 -w0 /usr/local/etc/php/php.ini")
		if err != nil {
			Fail(w, http.StatusInternalServerError, "读取 php.ini 失败: "+err.Error())
			return
		}
		raw, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if derr != nil {
			Fail(w, http.StatusInternalServerError, "php.ini 解码失败")
			return
		}
		OK(w, map[string]string{"content": string(raw)})
	case "php-ini-file-save":
		if !isPHPRuntimeKey(key) {
			Fail(w, http.StatusBadRequest, "仅 PHP 支持配置编辑")
			return
		}
		content := req.Content
		if len(content) == 0 || len(content) > 512*1024 {
			Fail(w, http.StatusBadRequest, "配置内容为空或超过 512KB")
			return
		}
		if strings.ContainsRune(content, 0) {
			Fail(w, http.StatusBadRequest, "配置包含非法字符")
			return
		}
		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		if _, err := dockerx.ExecIn(container, "sh", "-c", "echo "+b64+" | base64 -d > /usr/local/etc/php/php.ini"); err != nil {
			Fail(w, http.StatusInternalServerError, "写入 php.ini 失败: "+err.Error())
			return
		}
		if _, err := util.RunWithTimeout(60*time.Second, "docker", "restart", container); err != nil {
			Fail(w, http.StatusInternalServerError, "重启容器失败")
			return
		}
		OK(w, "php.ini 已保存并重启 PHP 生效")
	case "php-ini":
		if !isPHPRuntimeKey(key) {
			Fail(w, http.StatusBadRequest, "仅 PHP 支持设置修改")
			return
		}
		var lines []string
		for k, v := range req.Settings {
			if strings.ContainsAny(k, "\r\n") || strings.ContainsAny(v, "\r\n") {
				Fail(w, http.StatusBadRequest, "设置包含非法字符")
				return
			}
			lines = append(lines, k+" = "+v)
		}
		content := "; 由坤面板生成\n" + strings.Join(lines, "\n") + "\n"
		if _, err := dockerx.ExecIn(container, "sh", "-c", "mkdir -p /usr/local/etc/php/conf.d"); err != nil {
			Fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, err := dockerx.ExecIn(container, "sh", "-c", "cat > /usr/local/etc/php/conf.d/zz-kunpanel.ini <<'KPEOF'\n"+content+"KPEOF"); err != nil {
			Fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, err := dockerx.ExecIn(container, "sh", "-c", "docker-php-ext-enable opcache >/dev/null 2>&1; true"); err != nil {
			// 忽略
		}
		if _, err := util.RunWithTimeout(60*time.Second, "docker", "restart", container); err != nil {
			Fail(w, http.StatusInternalServerError, "重启容器失败")
			return
		}
		OK(w, "设置已保存并重启 PHP 生效")
	case "restart":
		if _, err := util.RunWithTimeout(60*time.Second, "docker", "restart", container); err != nil {
			Fail(w, http.StatusInternalServerError, "重启失败")
			return
		}
		OK(w, "已重启")
	case "modules":
		// nginx -V 输出在 stderr,ExecIn 为 CombinedOutput 可取到
		if key != "nginx" && key != "openresty" {
			Fail(w, http.StatusBadRequest, "仅 Nginx/OpenResty 支持")
			return
		}
		out, err := dockerx.ExecIn(container, "nginx", "-V")
		if err != nil && strings.TrimSpace(out) == "" {
			Fail(w, http.StatusInternalServerError, "读取模块信息失败: "+err.Error())
			return
		}
		OK(w, map[string]string{"raw": out})
	case "status":
		// 负载状态:自动装配 127.0.0.1 stub_status 端点(仅容器内可达)后轮询
		if key != "nginx" && key != "openresty" {
			Fail(w, http.StatusBadRequest, "仅 Nginx/OpenResty 支持")
			return
		}
		if _, err := dockerx.ExecIn(container, "sh", "-c", "test -f /etc/nginx/conf.d/kunstatus.conf"); err != nil {
			conf := "server {\n    listen 127.0.0.1:9100;\n    server_name _;\n    location /stub_status { stub_status; access_log off; allow 127.0.0.1; deny all; }\n}\n"
			if _, e := dockerx.ExecIn(container, "sh", "-c", "printf %s '"+conf+"' > /etc/nginx/conf.d/kunstatus.conf && nginx -t && nginx -s reload"); e != nil {
				Fail(w, http.StatusInternalServerError, "状态端点装配失败: "+e.Error())
				return
			}
		}
		raw, err := dockerx.ExecIn(container, "sh", "-c", "curl -s -m 3 http://127.0.0.1:9100/stub_status 2>/dev/null || wget -qO- -T 3 http://127.0.0.1:9100/stub_status 2>/dev/null")
		if err != nil || strings.TrimSpace(raw) == "" {
			OK(w, map[string]string{"raw": "", "error": "状态端点暂不可用(可能正在重载)"})
			return
		}
		OK(w, map[string]string{"raw": raw})
	case "conf-get":
		if key != "nginx" && key != "openresty" {
			Fail(w, http.StatusBadRequest, "仅 Nginx/OpenResty 支持")
			return
		}
		content, err := dockerx.ExecIn(container, "cat", "/etc/nginx/nginx.conf")
		if err != nil {
			Fail(w, http.StatusInternalServerError, "读取 nginx.conf 失败: "+err.Error())
			return
		}
		OK(w, map[string]string{"content": content})
	case "conf-save":
		if key != "nginx" && key != "openresty" {
			Fail(w, http.StatusBadRequest, "仅 Nginx/OpenResty 支持")
			return
		}
		content := req.Content
		if strings.TrimSpace(content) == "" || len(content) > 512*1024 || strings.ContainsRune(content, 0) {
			Fail(w, http.StatusBadRequest, "配置内容为空、过大或含非法字符")
			return
		}
		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		// 先备份 → 写入 → nginx -t 校验,失败自动回滚,通过则平滑重载
		script := "cp /etc/nginx/nginx.conf /etc/nginx/nginx.conf.bak && " +
			"echo " + b64 + " | base64 -d > /etc/nginx/nginx.conf && " +
			"(nginx -t 2>/tmp/kuntest && nginx -s reload || (cp /etc/nginx/nginx.conf.bak /etc/nginx/nginx.conf; cat /tmp/kuntest; exit 1))"
		out, err := dockerx.ExecIn(container, "sh", "-c", script)
		if err != nil {
			Fail(w, http.StatusBadRequest, "配置校验失败,已自动回滚: "+strings.TrimSpace(out))
			return
		}
		OK(w, "nginx.conf 已保存并平滑重载")
	case "errorlog":
		if key != "nginx" && key != "openresty" {
			Fail(w, http.StatusBadRequest, "仅 Nginx/OpenResty 支持")
			return
		}
		out, _ := dockerx.ExecIn(container, "sh", "-c", "tail -n 120 /var/log/nginx/error.log 2>/dev/null | base64 -w0")
		decoded, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
		if derr != nil {
			decoded = []byte("")
		}
		OK(w, map[string]string{"content": string(decoded)})
	case "errorlog-clear":
		if key != "nginx" && key != "openresty" {
			Fail(w, http.StatusBadRequest, "仅 Nginx/OpenResty 支持")
			return
		}
		// 截断清空(nginx 持有文件句柄,truncate 不影响运行,无需重载)
		if _, err := dockerx.ExecIn(container, "sh", "-c", ": > /var/log/nginx/error.log 2>/dev/null; echo cleared"); err != nil {
			Fail(w, http.StatusInternalServerError, "清空失败: "+err.Error())
			return
		}
		OK(w, "错误日志已清空")
	default:
		Fail(w, http.StatusBadRequest, "未知操作")
	}
}

// handleCustomAdd 新增自定义应用
func (s *Server) handleCustomAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Desc    string `json:"desc"`
		Compose string `json:"compose"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	t, err := appstore.AddCustomTemplate(req.Name, req.Desc, req.Compose)
	if err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, t)
}

// handleCustomDelete 删除自定义应用(须先卸载实例)
func (s *Server) handleCustomDelete(w http.ResponseWriter, r *http.Request) {
	key, ok := safeID(r.PathValue("key"))
	if !ok {
		Fail(w, http.StatusBadRequest, "非法应用标识")
		return
	}
	if err := appstore.RemoveCustomTemplate(key); err != nil {
		Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	OK(w, nil)
}
