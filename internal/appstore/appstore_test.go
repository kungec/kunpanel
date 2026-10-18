package appstore

import (
	"strings"
	"testing"
)

// PHP 多版本实例键判定:php / php+数字 为实例键;phpmyadmin 与带字母后缀的不是
func TestIsPHPRuntimeKey(t *testing.T) {
	yes := []string{"php", "php81", "php82", "php84"}
	no := []string{"phpmyadmin", "php8a", "phpmy", "mysql", "nginx", "php82x", ""}
	for _, k := range yes {
		if !IsPHPRuntimeKey(k) {
			t.Errorf("IsPHPRuntimeKey(%q) = false, want true", k)
		}
	}
	for _, k := range no {
		if IsPHPRuntimeKey(k) {
			t.Errorf("IsPHPRuntimeKey(%q) = true, want false", k)
		}
	}
}

// 实例键 → 模板键:PHP 多版本实例共用 php 模板
func TestTemplateKeyOf(t *testing.T) {
	if got := templateKeyOf("php82"); got != "php" {
		t.Errorf("templateKeyOf(php82) = %q, want php", got)
	}
	if got := templateKeyOf("php"); got != "php" {
		t.Errorf("templateKeyOf(php) = %q, want php", got)
	}
	if got := templateKeyOf("nginx"); got != "nginx" {
		t.Errorf("templateKeyOf(nginx) = %q, want nginx", got)
	}
	if _, ok := GetTemplate(templateKeyOf("php82")); !ok {
		t.Fatal("php82 的模板键解析后找不到 php 模板")
	}
}

// 多版本实例 compose 渲染必须替换容器名,否则自动修复会生成 kun-php 撞名首个实例
func TestRenderComposeForPHPMultiVersion(t *testing.T) {
	tpl, ok := GetTemplate("php")
	if !ok {
		t.Fatal("php 模板不存在")
	}
	params := map[string]string{"Version": "8.2", "Port": "8089"}
	content := renderComposeFor("php82", tpl, params)
	if !strings.Contains(content, "container_name: kun-php82") {
		t.Error("php82 compose 未替换容器名为 kun-php82")
	}
	if !strings.Contains(content, "php:8.2-apache") {
		t.Error("php82 compose 镜像版本不对,应为 php:8.2-apache")
	}
	if strings.Contains(content, "container_name: kun-php\n") {
		t.Error("php82 compose 仍含 kun-php 容器名,会与首个实例撞名")
	}
	// 首个实例保持 kun-php
	main := renderComposeFor("php", tpl, params)
	if !strings.Contains(main, "container_name: kun-php\n") {
		t.Error("php 实例 compose 容器名应为 kun-php")
	}
}

// 定制 PHP 镜像的 Dockerfile 生成分版本断言:
// - EOL 归档源兜底所有版本都要带上(stretch/buster 归档, bullseye 随时可能归档)
// - gd 编译参数 7.4 前后改名;libzip-dev 仅 7.4+ 需要
// - 5.6 等老镜像不用 -j(老 docker-php-ext-install 可能不识别)
// - preserve 模式编入旧容器配置与扩展
func TestGeneratePHPDockerfile(t *testing.T) {
	d56 := GeneratePHPDockerfile("5.6", false)
	if !strings.Contains(d56, "archive.debian.org") {
		t.Error("Dockerfile 应包含 EOL 归档源兜底")
	}
	if !strings.Contains(d56, "php:5.6-apache") {
		t.Error("基础镜像应为 php:5.6-apache")
	}
	if !strings.Contains(d56, "--with-freetype-dir") {
		t.Error("PHP <7.4 的 gd 应使用旧编译参数")
	}
	if strings.Contains(d56, "libzip-dev") {
		t.Error("PHP <7.4 的 zip 用内置 libzip,不应装 libzip-dev")
	}
	if strings.Contains(d56, `-j"$(nproc)"`) {
		t.Error("老镜像不应使用 -j 参数")
	}
	if !strings.Contains(d56, "redis-4.3.0") {
		t.Error("PHP 5.6 的 redis 应钉住 4.3.0")
	}

	d84 := GeneratePHPDockerfile("8.4", false)
	if !strings.Contains(d84, "--with-freetype --with-jpeg") {
		t.Error("PHP >=7.4 的 gd 应使用新编译参数")
	}
	if !strings.Contains(d84, "libzip-dev") {
		t.Error("PHP >=7.4 的 zip 需要 libzip-dev")
	}
	if !strings.Contains(d84, "pecl -q install redis") {
		t.Error("PHP 8.x 的 redis 应用最新版")
	}

	dp := GeneratePHPDockerfile("8.2", true)
	if !strings.Contains(dp, "COPY php_etc/ /usr/local/etc/php/") || !strings.Contains(dp, "COPY php_ext/") {
		t.Error("preserve 模式应编入旧容器配置与扩展")
	}
	if strings.Contains(dp, "pecl -q install redis") {
		t.Error("preserve 模式下 redis 应由 COPY 保留,不应重复安装")
	}

	if phpVerID("8.4") != 80400 || phpVerID("5.6") != 50600 || phpVerID("7.4") != 70400 {
		t.Errorf("phpVerID 版本换算不对: %d/%d/%d", phpVerID("8.4"), phpVerID("5.6"), phpVerID("7.4"))
	}
}

// compose 渲染的镜像行替换:无 docker 环境(PHPComposeImage 返回空)时保持官方镜像
func TestRenderComposePHPOfficialFallback(t *testing.T) {
	tpl, ok := GetTemplate("php")
	if !ok {
		t.Fatal("php 模板不存在")
	}
	content := renderComposeFor("php82", tpl, map[string]string{"Version": "8.2", "Port": "8088"})
	if !strings.Contains(content, "image: php:8.2-apache") {
		t.Error("无定制镜像时应保持官方镜像 php:8.2-apache")
	}
	if !strings.Contains(content, "container_name: kun-php82") {
		t.Error("php82 实例容器名应为 kun-php82")
	}
}
