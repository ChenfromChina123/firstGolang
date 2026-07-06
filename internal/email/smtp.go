// Package email 提供 SMTP 邮件发送功能（支持 SSL/TLS 直连，如阿里云企业邮箱 465 端口）
package email

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTPMailer 封装 SMTP 连接配置，支持异步发送邮件
type SMTPMailer struct {
	host     string
	port     int
	user     string
	password string
	from     string
}

// NewSMTPMailer 创建 SMTPMailer。
//   - host: SMTP 服务器地址（如 smtp.qiye.aliyun.com）
//   - port: 端口（465 用 SSL 直连，587 用 STARTTLS）
//   - user: 发件账号
//   - password: 发件密码/授权码
//   - from: 发件人地址（为空时用 user）
func NewSMTPMailer(host string, port int, user, password, from string) *SMTPMailer {
	if from == "" {
		from = user
	}
	return &SMTPMailer{
		host:     host,
		port:     port,
		user:     user,
		password: password,
		from:     from,
	}
}

// sendMailWithSSL 通过 SSL/TLS 直连发送邮件（适用于 465 端口）
// 标准库 net/smtp 不直接支持 SSL 直连，需要用 tls.Dial 包装
func (m *SMTPMailer) sendMailWithSSL(to, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", m.host, m.port)
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: m.host})
	if err != nil {
		return fmt.Errorf("tls dial %s: %w", addr, err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, m.host)
	if err != nil {
		return fmt.Errorf("smtp new client: %w", err)
	}
	defer client.Quit()

	auth := smtp.PlainAuth("", m.user, m.password, m.host)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}

	if err := client.Mail(m.from); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	defer w.Close()

	msg := buildMessage(m.from, to, subject, body)
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	return nil
}

// buildMessage 构造符合 RFC 822 的邮件内容（支持 HTML body）
func buildMessage(from, to, subject, body string) string {
	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + to + "\r\n")
	sb.WriteString("Subject: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(subject)) + "?=\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	sb.WriteString("Content-Transfer-Encoding: base64\r\n")
	sb.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(base64.StdEncoding.EncodeToString([]byte(body)))
	return sb.String()
}

// SendActivationEmailAsync 异步发送账号激活邮件。
// 使用 goroutine 避免阻塞 HTTP 请求；失败仅记录日志。
func (m *SMTPMailer) SendActivationEmailAsync(to, activationURL string) {
	go func() {
		subject := "FileSync 账号激活"
		body := fmt.Sprintf(`
<div style="font-family: Arial, sans-serif; max-width: 600px; margin: 0 auto; padding: 20px;">
  <h2 style="color: #00d9ff;">FileSync 账号激活</h2>
  <p>您好，请点击下方链接激活您的账号：</p>
  <p><a href="%s" style="display: inline-block; padding: 10px 20px; background: #00d9ff; color: #fff; text-decoration: none; border-radius: 4px;">激活账号</a></p>
  <p style="color: #999; font-size: 12px;">链接 24 小时内有效。如非本人操作，请忽略此邮件。</p>
  <p style="color: #999; font-size: 12px;">若按钮无法点击，请直接访问：%s</p>
</div>
`, activationURL, activationURL)
		if err := m.sendMailWithSSL(to, subject, body); err != nil {
			log.Printf("[Email] send activation email failed: to=%s err=%v", to, err)
		} else {
			log.Printf("[Email] activation email sent: to=%s", to)
		}
	}()
}

// SendResetCodeEmailAsync 异步发送密码重置验证码邮件。
// 使用 goroutine 避免阻塞 HTTP 请求；失败仅记录日志。
func (m *SMTPMailer) SendResetCodeEmailAsync(to, code string) {
	go func() {
		subject := "FileSync 密码重置验证码"
		body := fmt.Sprintf(`
<div style="font-family: Arial, sans-serif; max-width: 600px; margin: 0 auto; padding: 20px;">
  <h2 style="color: #00d9ff;">FileSync 密码重置</h2>
  <p>您好，您的密码重置验证码为：</p>
  <p style="font-size: 32px; font-weight: bold; color: #00d9ff; letter-spacing: 8px;">%s</p>
  <p style="color: #999; font-size: 12px;">验证码 10 分钟内有效。如非本人操作，请忽略此邮件。</p>
</div>
`, code)
		if err := m.sendMailWithSSL(to, subject, body); err != nil {
			log.Printf("[Email] send reset code email failed: to=%s err=%v", to, err)
		} else {
			log.Printf("[Email] reset code email sent: to=%s", to)
		}
	}()
}

// Host 返回 SMTP 主机地址（用于日志和测试）
func (m *SMTPMailer) Host() string {
	return net.JoinHostPort(m.host, fmt.Sprintf("%d", m.port))
}
