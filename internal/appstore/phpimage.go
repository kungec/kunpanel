package appstore

// PHP 定制镜像:安装/重建时从官方 php:<版本>-apache 构建内置常用扩展的
// kunpanel/php:<版本>-apache。
//   ① 常用扩展(gd/zip/intl/bcmath/redis 等)默认内置,免逐个后装;
//   ② EOL 版本(5.6=stretch,7.4/8.0=buster)官方软件源已归档,构建时自动
//      切换 archive.debian.org,老版本也能装齐依赖;
//   ③ 重建时把旧容器的 /usr/local/etc/php(php.ini/conf.d)与扩展 .so 编入
//      新镜像,升级不丢配置与已装扩展。

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"kunpanel/internal/util"
)

// DebArchiveFallbackSh EOL Debian 套件源归档兜底:默认源 update 失败时自动切换
// archive.debian.org。归档套件的 Release 有效期与签名密钥均已过期,需 [trusted=yes]
// + 关闭 Check-Valid-Until,否则 apt 拒装未认证包。套件代号从 /etc/os-release
// 提取——stretch 等老版本没有 VERSION_CODENAME 字段,回退解析 VERSION="9 (stretch)";
// 在 Dockerfile RUN 与 docker exec sh -c 中通用
const DebArchiveFallbackSh = `if ! apt-get update -qq; then CODENAME="$(sed -n 's/^VERSION_CODENAME=//p' /etc/os-release | tr -d '"')"; if [ -z "$CODENAME" ]; then CODENAME="$(sed -n 's/^VERSION=//p' /etc/os-release | tr -d '"' | sed -n 's/^[^(]*(\([^)]*\)).*/\1/p')"; fi; printf 'deb [trusted=yes] http://archive.debian.org/debian %s main contrib non-free\n' "$CODENAME" > /etc/apt/sources.list; printf 'deb [trusted=yes] http://archive.debian.org/debian-security %s/updates main contrib non-free\n' "$CODENAME" >> /etc/apt/sources.list; echo 'Acquire::Check-Valid-Until "false";' > /etc/apt/apt.conf.d/99-kun-archive; apt-get update -qq; fi`

// phpBakedDeps 内置扩展所需的系统库(gd/intl/soap/bz2/ftp 等)
const phpBakedDeps = "libfreetype6-dev libjpeg62-turbo-dev libpng-dev libbz2-dev libicu-dev libxml2-dev libssl-dev"

// phpBakedExts 内置扩展清单:覆盖绝大多数业务需要(验证码/缩略图/压缩/国际化/
// 数据库/多进程),均为源码树内置扩展,官方 php 镜像默认未编译
const phpBakedExts = "opcache bcmath calendar dba exif ftp gettext intl mysqli pdo_mysql pcntl shmop soap sockets sysvmsg sysvsem sysvshm zip bz2 gd"

// BakedImageTag PHP 定制镜像标签
func BakedImageTag(version string) string {
	return "kunpanel/php:" + strings.TrimSpace(version) + "-apache"
}

// phpVerID "8.4" → 80400(对齐 PHP_VERSION_ID 语义)
func phpVerID(version string) int {
	parts := strings.Split(strings.TrimSpace(version), ".")
	maj, _ := strconv.Atoi(parts[0])
	min := 0
	if len(parts) > 1 {
		min, _ = strconv.Atoi(parts[1])
	}
	return maj*10000 + min*100
}

// GeneratePHPDockerfile 生成定制镜像 Dockerfile;preserve=true 时把构建上下文里
// 的 php_etc/(旧容器 /usr/local/etc/php)与 php_ext/(扩展 .so)编入镜像
func GeneratePHPDockerfile(version string, preserve bool) string {
	deps := phpBakedDeps
	gdConf := "--with-freetype --with-jpeg"
	jFlag := `-j"$(nproc)" `
	if phpVerID(version) >= 70400 {
		// 7.4 起 zip 扩展不再内置 libzip,需系统库
		deps += " libzip-dev"
	} else {
		// <7.4 的 gd 用旧参数名,且老镜像的 docker-php-ext-install 未必支持 -j
		gdConf = "--with-freetype-dir=/usr/include/ --with-jpeg-dir=/usr/include/"
		jFlag = ""
	}
	preserveBlock := ""
	if preserve {
		preserveBlock = "\n# 保留旧容器的 PHP 配置与扩展(php.ini/conf.d + 编译产物,含 pecl 扩展)\nCOPY php_etc/ /usr/local/etc/php/\nCOPY php_ext/ /usr/local/lib/php/extensions/\n"
	}
	dockerfile := "FROM php:" + version + "-apache\n" +
		"\n# EOL 版本自动切换归档软件源并安装依赖(独立一层:编译层失败重试时复用缓存)\n" +
		"RUN set -eux; " + DebArchiveFallbackSh + "; " +
		"DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends " + deps + "; " +
		"rm -rf /var/lib/apt/lists/*\n" +
		"\n# 常用扩展默认内置\n" +
		"RUN set -eux; " +
		"docker-php-ext-configure gd " + gdConf + "; " +
		"docker-php-ext-install " + jFlag + phpBakedExts + "; " +
		"docker-php-source delete\n"
	// pecl redis 按 PHP 版本钉住发行版(最新版不支持老 PHP);保留模式下由 COPY 带入
	if !preserve {
		dockerfile += "\nRUN set -eux; if command -v pecl >/dev/null 2>&1; then PHPID=$(php -r 'echo PHP_VERSION_ID;'); if [ \"$PHPID\" -ge 80000 ]; then pecl -q install redis; elif [ \"$PHPID\" -ge 70100 ]; then pecl -q install redis-5.3.7; else pecl -q install redis-4.3.0; fi; docker-php-ext-enable redis; fi\n"
	}
	return dockerfile + preserveBlock
}

// phpImageExists 镜像是否已构建(docker CLI 不可用视为否)
func phpImageExists(tag string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "image", "inspect", tag).Run() == nil
}

// PHPComposeImage PHP 实例应使用的镜像:定制镜像已构建则用之,否则返回空串
// (调用方沿用官方镜像)
func PHPComposeImage(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}
	if tag := BakedImageTag(version); phpImageExists(tag) {
		return tag
	}
	return ""
}

// phpInstance 按键取实例
func phpInstance(key string) *Instance {
	list := loadInstances()
	for i := range list {
		if list[i].Key == key {
			return &list[i]
		}
	}
	return nil
}

// EnsurePHPImage 确保 PHP 定制镜像存在;force=true 强制重建。
// preserveFrom 为容器名时,把它的 PHP 配置与扩展 .so 编入新镜像
func EnsurePHPImage(version string, preserveFrom string, force bool, w io.Writer) error {
	if w == nil {
		w = io.Discard
	}
	tag := BakedImageTag(version)
	if !force && phpImageExists(tag) {
		fmt.Fprintf(w, ">> 定制镜像 %s 已存在,跳过构建\n", tag)
		return nil
	}
	fmt.Fprintf(w, ">> 构建 PHP 定制镜像 %s(内置 gd/zip/intl/bcmath/redis 等常用扩展)\n", tag)
	fmt.Fprintln(w, ">> 同一版本仅需构建一次,约 5~10 分钟,请耐心等待 ...")
	tmp, err := os.MkdirTemp("", "kunphpimg-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if preserveFrom != "" {
		fmt.Fprintf(w, ">> 保留 %s 的 php.ini/conf.d 与已装扩展 ...\n", preserveFrom)
		etcDir, extDir := filepath.Join(tmp, "php_etc"), filepath.Join(tmp, "php_ext")
		_ = os.MkdirAll(etcDir, 0o755)
		_ = os.MkdirAll(extDir, 0o755)
		if _, e := util.RunWithTimeout(120*time.Second, "docker", "cp", preserveFrom+":/usr/local/etc/php/.", etcDir); e != nil {
			fmt.Fprintln(w, ">> (旧配置保留失败,将不带旧配置构建)", e)
		}
		if _, e := util.RunWithTimeout(120*time.Second, "docker", "cp", preserveFrom+":/usr/local/lib/php/extensions/.", extDir); e != nil {
			fmt.Fprintln(w, ">> (旧扩展保留失败,不影响内置扩展)", e)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "Dockerfile"), []byte(GeneratePHPDockerfile(version, preserveFrom != "")), 0o644); err != nil {
		return err
	}
	if err := util.RunWithTimeoutWriter(30*time.Minute, w, "docker", "build", "-t", tag, tmp); err != nil {
		return fmt.Errorf("定制镜像构建失败: %v", err)
	}
	fmt.Fprintf(w, ">> 定制镜像构建完成: %s\n", tag)
	return nil
}

// syncPHPComposeImage compose 仍指向官方镜像且定制镜像已就绪时,把 image 行
// 切换到定制镜像(只动这一行,不碰用户其他自定义)
func syncPHPComposeImage(key, version string) error {
	img := PHPComposeImage(version)
	if img == "" {
		return nil
	}
	dir := InstanceDir(key)
	b, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		return err
	}
	official := "image: php:" + version + "-apache"
	if !strings.Contains(string(b), official) {
		return nil
	}
	return util.AtomicWriteString(filepath.Join(dir, "docker-compose.yml"),
		strings.Replace(string(b), official, "image: "+img, 1), 0o644)
}

// PreparePHPStart PHP 实例拉起前准备:确保定制镜像并同步 compose 的 image 行。
// 非 PHP 实例直接返回
func PreparePHPStart(key string, w io.Writer) error {
	if !IsPHPRuntimeKey(key) {
		return nil
	}
	inst := phpInstance(key)
	if inst == nil {
		return nil
	}
	ver := strings.TrimSpace(inst.Params["Version"])
	if ver == "" {
		return nil
	}
	if err := EnsurePHPImage(ver, "", false, w); err != nil {
		return err
	}
	return syncPHPComposeImage(key, ver)
}

// RebuildPHPImage 重建 PHP 定制镜像(保留现有配置与已装扩展)并重建容器,短暂中断
func RebuildPHPImage(key string, w io.Writer) error {
	if w == nil {
		w = io.Discard
	}
	if !IsPHPRuntimeKey(key) {
		return fmt.Errorf("仅 PHP 运行时支持重建镜像")
	}
	inst := phpInstance(key)
	if inst == nil {
		return fmt.Errorf("实例不存在")
	}
	ver := strings.TrimSpace(inst.Params["Version"])
	if ver == "" {
		return fmt.Errorf("实例没有版本参数")
	}
	if err := EnsurePHPImage(ver, "kun-"+key, true, w); err != nil {
		return err
	}
	if err := syncPHPComposeImage(key, ver); err != nil {
		return err
	}
	fmt.Fprintln(w, ">> 用新镜像重建容器(站点文件在外部挂载,数据不受影响)...")
	if err := Apply(key, "up -d", w); err != nil {
		return err
	}
	fmt.Fprintf(w, ">> 完成。当前内置扩展: %s redis\n", phpBakedExts)
	return nil
}
