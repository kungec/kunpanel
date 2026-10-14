package plugins

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kunpanel/internal/appstore"
	"kunpanel/internal/config"
	"kunpanel/internal/dockerx"
	"kunpanel/internal/util"
)

// nginxLuaImage 面板自编译的 nginx + Lua 引擎镜像(等价 OpenResty 能力,保留 nginx 身份)
const nginxLuaImage = "kunpanel/nginx-lua:1.27.1.2"

// nginxLuaDockerfile 多阶段构建:用 OpenResty 完整源码编译出带 Lua 的 nginx,
// 再把产物装进官方 nginx 镜像(布局 /etc/nginx 不变,站点配置/挂载零改动继承)。
const nginxLuaDockerfile = `# 坤面板 WAF 专用镜像:nginx + Lua 规则引擎(OpenResty 源码编译,保留官方 nginx 镜像布局)
FROM debian:bookworm-slim AS build
ARG OR_VER=1.27.1.2
RUN apt-get update && apt-get install -y --no-install-recommends \
        make gcc libc6-dev libpcre2-dev zlib1g-dev libssl-dev \
        perl wget ca-certificates xz-utils \
    && wget -q https://openresty.org/download/openresty-${OR_VER}.tar.gz \
    && tar xzf openresty-${OR_VER}.tar.gz \
    && cd openresty-${OR_VER} \
    && ./configure --prefix=/usr/local/openresty \
        --with-http_ssl_module --with-http_v2_module --with-http_realip_module \
        --with-http_stub_status_module --with-http_gzip_static_module --with-http_sub_module \
        --with-stream --with-stream_ssl_module --with-pcre-jit \
    && make -j"$(nproc)" \
    && make install

FROM nginx:1.29
COPY --from=build /usr/local/openresty /usr/local/openresty
# nginx 可执行文件替换为 Lua 版;编译前缀 /usr/local/openresty/nginx/conf 指回 /etc/nginx,
# 使官方镜像的 CMD(nginx -g daemon off)与现有挂载(/etc/nginx/conf.d 等)原样生效
RUN ln -sf /usr/local/openresty/nginx/sbin/nginx /usr/sbin/nginx \
    && rm -rf /usr/local/openresty/nginx/conf \
    && ln -s /etc/nginx /usr/local/openresty/nginx/conf \
    && echo /usr/local/openresty/luajit/lib > /etc/ld.so.conf.d/90-luajit.conf \
    && ldconfig
ENV PATH=/usr/local/openresty/luajit/bin:/usr/local/openresty/nginx/sbin:$PATH \
    LUA_PATH="/usr/local/openresty/lualib/?.lua;;" \
    LUA_CPATH="/usr/local/openresty/lualib/?.so;;"
`

// NginxLuaReady kun-nginx 是否已运行 WAF 版镜像(面板自编译 nginx+Lua)
func NginxLuaReady() bool {
	out, err := util.RunWithTimeout(10*time.Second, "docker", "inspect", "-f", "{{.Config.Image}}", "kun-nginx")
	return err == nil && strings.Contains(out, "kunpanel/nginx-lua")
}

// UpgradeNginxForWaf 把官方 nginx 容器原位升级为 nginx+Lua 镜像:
// 构建面板自编译镜像 → 替换 compose 中的 image → 重建容器(容器名/端口/挂载/站点配置全部保留)
func UpgradeNginxForWaf(w io.Writer) error {
	if NginxLuaReady() {
		return nil
	}
	// 1) 构建镜像(有缓存时秒级完成)
	buildDir := filepath.Join(config.DataDir(), "plugins", "waf", "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}
	df := filepath.Join(buildDir, "Dockerfile")
	if old, e := os.ReadFile(df); e != nil || string(old) != nginxLuaDockerfile {
		if err := os.WriteFile(df, []byte(nginxLuaDockerfile), 0o644); err != nil {
			return err
		}
	}
	if out, e := util.RunWithTimeout(10*time.Second, "docker", "image", "inspect", nginxLuaImage); e != nil || strings.TrimSpace(out) == "" {
		fmt.Fprintln(w, "[升级] 编译 nginx + Lua 引擎(首次约 5~8 分钟,请耐心等待)...")
		if err := util.RunWithTimeoutWriter(30*time.Minute, w, "docker", "build", "--progress=plain", "-t", nginxLuaImage, buildDir); err != nil {
			return fmt.Errorf("nginx+Lua 镜像构建失败: %v", err)
		}
		fmt.Fprintln(w, "[升级] 镜像构建完成: "+nginxLuaImage)
	} else {
		fmt.Fprintln(w, "[升级] 使用已构建的镜像: "+nginxLuaImage)
	}
	// 2) 替换 compose 中的 image(容器名 kun-nginx、端口、挂载均不变)
	dir := appstore.InstanceDir("nginx")
	composeFile := filepath.Join(dir, "docker-compose.yml")
	b, err := os.ReadFile(composeFile)
	if err != nil {
		return fmt.Errorf("读取 nginx compose 失败: %v", err)
	}
	re := regexp.MustCompile(`(?m)^(\s*image:\s*)nginx:\S+\s*$`)
	content := re.ReplaceAllString(string(b), "${1}"+nginxLuaImage)
	if !strings.Contains(content, nginxLuaImage) {
		return fmt.Errorf("nginx compose 中未找到 image 行,请卸载后重新安装 Nginx")
	}
	if content != string(b) {
		if err := os.WriteFile(composeFile, []byte(content), 0o644); err != nil {
			return err
		}
	}
	// 3) 重建容器(同 compose,仅镜像变化)
	fmt.Fprintln(w, "[升级] 用 nginx+Lua 镜像重建容器(站点配置无缝继承)...")
	out, err := dockerx.Compose(dir, "up", "-d")
	if w != nil && strings.TrimSpace(out) != "" {
		fmt.Fprintln(w, strings.TrimSpace(out))
	}
	if err != nil {
		return fmt.Errorf("容器重建失败: %s", strings.TrimSpace(out))
	}
	for i := 0; i < 20; i++ {
		if dockerx.ContainerRunning("kun-nginx") {
			fmt.Fprintln(w, "[升级] nginx+Lua 已就绪(仍是 Nginx,站点无需任何改动)")
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("容器重建超时")
}

// gatewayContainer 当前网站网关容器名(kun-openresty 或 kun-nginx),空串表示未运行
func gatewayContainer() string {
	if dockerx.ContainerRunning("kun-openresty") {
		return "kun-openresty"
	}
	if dockerx.ContainerRunning("kun-nginx") {
		return "kun-nginx"
	}
	return ""
}

// ReloadGateway 平滑重载当前网关容器(openresty 优先,nginx 兜底)
func ReloadGateway() (string, error) {
	gw := gatewayContainer()
	if gw == "" {
		return "", fmt.Errorf("未检测到运行中的 Nginx / OpenResty 容器")
	}
	return util.RunWithTimeout(30*time.Second, "docker", "exec", gw, "nginx", "-s", "reload")
}
