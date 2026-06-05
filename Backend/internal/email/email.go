package email

import (
	"fmt"
	"net/smtp"
	"strings"
)

type Sender struct {
	host     string
	port     string
	user     string
	password string
	from     string
}

func NewSender(host, port, user, password, from string) *Sender {
	return &Sender{host: host, port: port, user: user, password: password, from: from}
}

func (s *Sender) Send(to, subject, body string) error {
	if s.user == "" || s.password == "" {
		// Dev mode — just log
		fmt.Printf("[EMAIL] To: %s\nSubject: %s\n%s\n", to, subject, body)
		return nil
	}

	auth := smtp.PlainAuth("", s.user, s.password, s.host)

	msg := strings.Join([]string{
		"From: " + s.from,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		"",
		body,
	}, "\r\n")

	addr := s.host + ":" + s.port
	return smtp.SendMail(addr, auth, s.from, []string{to}, []byte(msg))
}

func (s *Sender) SendPasswordReset(to, resetURL string) error {
	body := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<body style="font-family:-apple-system,sans-serif;background:#0f0f0f;color:#e5e5e5;padding:40px 20px;text-align:center">
  <div style="max-width:480px;margin:0 auto;background:#161616;border:1px solid #2a2a2a;border-radius:16px;padding:40px">
    <div style="font-size:32px;margin-bottom:16px">⚡</div>
    <h1 style="font-size:22px;font-weight:600;margin:0 0 8px">Reset your password</h1>
    <p style="color:#888;font-size:14px;margin:0 0 32px;line-height:1.5">
      Click the button below to reset your password. This link expires in 1 hour.
    </p>
    <a href="%s" style="display:inline-block;background:#7c3aed;color:#fff;text-decoration:none;padding:12px 28px;border-radius:10px;font-weight:600;font-size:14px">
      Reset Password
    </a>
    <p style="color:#444;font-size:12px;margin:32px 0 0">
      If you didn't request this, you can safely ignore this email.
    </p>
  </div>
</body>
</html>`, resetURL)

	return s.Send(to, "Reset your Lovable password", body)
}
