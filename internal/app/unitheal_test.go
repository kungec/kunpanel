package app

import (
	"strings"
	"testing"
)

const oldUnit = `[Unit]
Description=KunPanel Server Panel
After=network.target

[Service]
Type=simple
ExecStart=/opt/kunpanel/bin/kunpanel
Restart=always
LimitNOFILE=65535
Environment=KP_DATA_DIR=/opt/kunpanel/data
# 安全加固
NoNewPrivileges=true
ProtectSystem=full
ReadWritePaths=/etc/ssh -/etc/sysctl.d /etc/ufw /etc/systemd/system
Environment="GOGC=50"
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true

[Install]
WantedBy=multi-user.target
`

func TestRemoveDeprecatedLines(t *testing.T) {
	out := removeDeprecatedLines(oldUnit)
	for _, bad := range []string{"ProtectSystem=", "ReadWritePaths=", "RestrictSUIDSGID="} {
		if strings.Contains(out, bad) {
			t.Errorf("改写后仍包含 %s:\n%s", bad, out)
		}
	}
	// 用户自定义与保留加固项不能动
	for _, keep := range []string{
		"ExecStart=/opt/kunpanel/bin/kunpanel",
		"Environment=KP_DATA_DIR=/opt/kunpanel/data",
		"NoNewPrivileges=true",
		"PrivateTmp=true",
		"ProtectKernelTunables=true",
		"ProtectControlGroups=true",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(out, keep) {
			t.Errorf("改写后丢失必要行 %s:\n%s", keep, out)
		}
	}
}

func TestRemoveDeprecatedLinesCleanUnitUnchanged(t *testing.T) {
	clean := removeDeprecatedLines(oldUnit)
	if again := removeDeprecatedLines(clean); again != clean {
		t.Errorf("修复后的单元再次改写应保持不变:\n%s", again)
	}
}
