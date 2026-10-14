// alertx/mail.go —— SMTP 邮件告警渠道:配置、发送、测试
package alertx

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"kunpanel/internal/notifyx"
	"kunpanel/internal/util"
)

// SMTP 配置扩展(挂在 Settings 上)
type SMTPSettings struct {
	SMTPHost string `json:"smtp_host"`
	SMTPPort int    `json:"smtp_port"` // 465=隐式 TLS / 587=STARTTLS / 25=明文
	SMTPUser string `json:"smtp_user"`
	SMTPPass string `json:"smtp_pass"`
	MailFrom string `json:"mail_from"`
	MailTo   string `json:"mail_to"` // 多个收件人用逗号分隔
}

// GetSMTP 读取邮件告警配置
func GetSMTP() SMTPSettings {
	s := Get() // Settings 内嵌 SMTPSettings 字段
	return s.SMTPSettings
}

var emailRe = func() func(string) bool {
	return func(addr string) bool {
		at := strings.Index(addr, "@")
		return at > 0 && at < len(addr)-1 && !strings.ContainsAny(addr, " \t;,")
	}
}()

// SaveSMTP 保存邮件告警配置
func SaveSMTP(s SMTPSettings) error {
	if s.SMTPHost == "" {
		// 留空表示关闭邮件渠道
		s = SMTPSettings{}
	} else {
		if s.SMTPPort != 25 && s.SMTPPort != 465 && s.SMTPPort != 587 {
			return fmt.Errorf("SMTP 端口仅支持 25 / 465 / 587")
		}
		if !emailRe(s.MailFrom) {
			return fmt.Errorf("发件人邮箱格式不正确")
		}
		for _, to := range strings.Split(s.MailTo, ",") {
			to = strings.TrimSpace(to)
			if to != "" && !emailRe(to) {
				return fmt.Errorf("收件人邮箱格式不正确: %s", to)
			}
		}
		if s.MailTo == "" {
			return fmt.Errorf("收件人不能为空")
		}
	}
	cur := Get()
	cur.SMTPSettings = s
	return Save(cur)
}

// SendMail 通过 SMTP 发送邮件(465 隐式 TLS,587 STARTTLS,25 明文)
func SendMail(subject, body string) error {
	cfg := GetSMTP()
	if cfg.SMTPHost == "" {
		return fmt.Errorf("未配置邮件服务器")
	}
	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, cfg.SMTPPort)
	from := cfg.MailFrom
	recipients := []string{}
	for _, to := range strings.Split(cfg.MailTo, ",") {
		if to = strings.TrimSpace(to); to != "" {
			recipients = append(recipients, to)
		}
	}
	if len(recipients) == 0 {
		return fmt.Errorf("收件人为空")
	}

	header := make([]string, 0, 6)
	header = append(header, "From: "+from)
	header = append(header, "To: "+strings.Join(recipients, ", "))
	header = append(header, mimeHeader("Subject", subject))
	header = append(header, "MIME-Version: 1.0")
	header = append(header, "Content-Type: text/plain; charset=UTF-8")
	header = append(header, "Date: "+time.Now().Format("2006-01-02 15:04:05"))
	msg := []byte(strings.Join(header, "\r\n") + "\r\n\r\n" + body + "\r\n")

	var auth smtp.Auth
	if cfg.SMTPUser != "" {
		host, _, _ := net.SplitHostPort(addr)
		auth = smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPass, host)
	}

	var client *smtp.Client
	var err error
	if cfg.SMTPPort == 465 {
		conn, e := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.SMTPHost})
		if e != nil {
			return fmt.Errorf("TLS 连接失败: %v", e)
		}
		client, err = smtp.NewClient(conn, cfg.SMTPHost)
		if err != nil {
			conn.Close()
			return err
		}
		defer client.Close()
	} else {
		client, err = smtp.Dial(addr)
		if err != nil {
			return fmt.Errorf("连接失败: %v", err)
		}
		defer client.Close()
		if ok, _ := client.Extension("STARTTLS"); ok {
			host, _, _ := net.SplitHostPort(addr)
			if err := client.StartTLS(&tls.Config{ServerName: host}); err != nil {
				return fmt.Errorf("STARTTLS 失败: %v", err)
			}
		}
	}
	if auth != nil {
		if ok, _ := client.Extension("AUTH"); ok {
			if err := client.Auth(auth); err != nil {
				return fmt.Errorf("SMTP 认证失败: %v", err)
			}
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range recipients {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("收件人 %s 被拒绝: %v", rcpt, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// TestMail 发送测试邮件
func TestMail() error {
	return SendMail("坤面板测试邮件",
		fmt.Sprintf("这是一封来自坤面板(%s)的测试邮件,收到说明邮件告警渠道配置成功。\n时间: %s", util.LocalIP(), time.Now().Format("2006-01-02 15:04:05")))
}

// Broadcast 同时向 Webhook 与邮件推送文本告警(upmon/证书巡检等模块复用)
// webhook 为空时仅发邮件;反之亦然
func Broadcast(webhook string, text string, extra map[string]any) {
	notifyx.Add("alert", "告警通知", text)
	payload := map[string]any{"text": text}
	for k, v := range extra {
		payload[k] = v
	}
	if webhook != "" {
		go func() { _ = Push(webhook, payload) }()
	}
	if cfg := GetSMTP(); cfg.SMTPHost != "" {
		go func() { _ = SendMail(text, text+"\n\n主机: "+util.LocalIP()) }()
	}
}

// mimeHeader 编码含中文的邮件头(RFC 2047 B 编码)
func mimeHeader(key, value string) string {
	needsEnc := false
	for _, r := range value {
		if r > 127 {
			needsEnc = true
			break
		}
	}
	if needsEnc {
		value = "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(value)) + "?="
	}
	return key + ": " + value
}
