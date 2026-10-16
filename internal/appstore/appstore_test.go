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
