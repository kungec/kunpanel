package server

// 面板 SSL 的服务器证书文件导入辅助:读取任意绝对路径的 PEM 证书/私钥文件
// (复制语义,之后源文件变动不影响面板)

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const pemFileMax = 2 << 20 // 单文件上限 2MB(证书/私钥远小于此)

// readPEMFiles 读取证书与私钥文件(需绝对路径),返回内容文本
func readPEMFiles(certPath, keyPath string) (string, string, error) {
	cp := strings.TrimSpace(certPath)
	kp := strings.TrimSpace(keyPath)
	if cp == "" || kp == "" {
		return "", "", fmt.Errorf("证书文件与私钥文件路径需同时提供")
	}
	if !filepath.IsAbs(cp) || !filepath.IsAbs(kp) {
		return "", "", fmt.Errorf("需使用绝对路径(以 / 开头)")
	}
	for _, p := range []string{cp, kp} {
		fi, err := os.Stat(p)
		if err != nil {
			return "", "", fmt.Errorf("文件不存在或不可读: %s", p)
		}
		if fi.Size() > pemFileMax {
			return "", "", fmt.Errorf("文件过大(超过 2MB),不是有效的证书/私钥文件: %s", p)
		}
	}
	cb, err := os.ReadFile(cp)
	if err != nil {
		return "", "", fmt.Errorf("读取证书文件失败: %v", err)
	}
	kb, err := os.ReadFile(kp)
	if err != nil {
		return "", "", fmt.Errorf("读取私钥文件失败: %v", err)
	}
	return string(cb), string(kb), nil
}
