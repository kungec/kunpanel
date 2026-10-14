package main

import (
	"embed"
	"flag"
	"fmt"
	"os"

	"kunpanel/internal/app"
)

// version 由构建时注入: -ldflags "-X main.version=v1.0.0"
var version = "v1.0.0"

//go:embed all:web
var webFS embed.FS

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Println("KunPanel", version)
			return
		case "reset":
			// 重置管理员密码:kunpanel reset
			if err := app.ResetPassword(); err != nil {
				fmt.Fprintln(os.Stderr, "重置失败:", err)
				os.Exit(1)
			}
			return
		}
	}

	cfgFile := flag.String("c", "", "配置文件路径(默认使用数据目录下的 config.json)")
	flag.Parse()

	if err := app.Run(app.Options{Version: version, ConfigFile: *cfgFile, WebFS: webFS}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
