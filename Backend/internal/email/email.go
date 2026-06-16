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
		fmt.Printf("\n[EMAIL DEV MODE]\nTo: %s\nSubject: %s\nBody: %s\n\n", to, subject, body)
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
	return smtp.SendMail(s.host+":"+s.port, auth, s.from, []string{to}, []byte(msg))
}

func wrap(icon, title, message, btnText, btnURL string) string {
	return fmt.Sprintf(`<!DOCTYPE html><html><body style="font-family:-apple-system,sans-serif;background:#0f0f0f;color:#e5e5e5;padding:40px 20px;text-align:center">
<div style="max-width:480px;margin:0 auto;background:#161616;border:1px solid #2a2a2a;border-radius:16px;padding:40px">
  <div style="font-size:36px;margin-bottom:16px">%s</div>
  <h1 style="font-size:22px;font-weight:600;margin:0 0 12px">%s</h1>
  <p style="color:#888;font-size:14px;line-height:1.6;margin:0 0 28px">%s</p>
  <a href="%s" style="display:inline-block;background:#7c3aed;color:#fff;text-decoration:none;padding:13px 32px;border-radius:10px;font-weight:600;font-size:14px">%s</a>
</div></body></html>`, icon, title, message, btnURL, btnText)
}

// ── Password Reset ───────────────────────────────────────────────────────────

func (s *Sender) SendPasswordReset(to, resetURL string) error {
	body := wrap("🔑",
		"Reset your password",
		"Click below to reset your Lovable password. This link expires in 1 hour.",
		"Reset Password",
		resetURL,
	)
	return s.Send(to, "Reset your Lovable password", body)
}

// ── Welcome ──────────────────────────────────────────────────────────────────

func (s *Sender) SendWelcome(to, name string) error {
	body := wrap("⚡",
		"Welcome to Lovable, "+name+"!",
		"You are all set. Start building apps with AI — just describe what you want and watch it come to life.",
		"Start Building",
		"http://localhost:5173/dashboard",
	)
	return s.Send(to, "Welcome to Lovable!", body)
}

// ── Project Invite ───────────────────────────────────────────────────────────

func (s *Sender) SendProjectInvite(to, inviterName, projectName, acceptURL string) error {
	msg := fmt.Sprintf("<strong style='color:#e5e5e5'>%s</strong> invited you to collaborate on the project <strong style='color:#7c3aed'>%s</strong>. This invitation expires in 7 days.", inviterName, projectName)
	body := wrap("👥",
		"You have been invited!",
		msg,
		"Accept Invitation",
		acceptURL,
	)
	return s.Send(to, inviterName+" invited you to "+projectName, body)
}

// ── Build Complete ───────────────────────────────────────────────────────────

func (s *Sender) SendBuildComplete(to, projectName, summary string) error {
	msg := fmt.Sprintf("Your project <strong style='color:#e5e5e5'>%s</strong> has been built successfully.<br><br>%s", projectName, summary)
	body := wrap("✅",
		"Build Complete!",
		msg,
		"View Project",
		"http://localhost:5173/dashboard",
	)
	return s.Send(to, "Build complete: "+projectName, body)
}

// ── Sandbox Expired ──────────────────────────────────────────────────────────

func (s *Sender) SendSandboxExpired(to, projectName string) error {
	msg := fmt.Sprintf("Your live preview for <strong style='color:#e5e5e5'>%s</strong> has expired after 1 hour. Launch a new sandbox to continue previewing.", projectName)
	body := wrap("⏰",
		"Sandbox Expired",
		msg,
		"Relaunch Sandbox",
		"http://localhost:5173/dashboard",
	)
	return s.Send(to, "Sandbox expired: "+projectName, body)
}

// ── Member Removed ───────────────────────────────────────────────────────────

func (s *Sender) SendRemovedFromProject(to, projectName string) error {
	msg := fmt.Sprintf("You have been removed from the project <strong style='color:#e5e5e5'>%s</strong>.", projectName)
	body := wrap("🚪",
		"Removed from project",
		msg,
		"Go to Dashboard",
		"http://localhost:5173/dashboard",
	)
	return s.Send(to, "You were removed from "+projectName, body)
}

// ── Weekly Summary ───────────────────────────────────────────────────────────

func (s *Sender) SendWeeklySummary(to, name string, projectCount, messageCount int) error {
	msg := fmt.Sprintf("Here is your Lovable activity this week, <strong style='color:#e5e5e5'>%s</strong>.<br><br>Projects: <strong>%d</strong> &nbsp;|&nbsp; AI Messages: <strong>%d</strong>", name, projectCount, messageCount)
	body := wrap("📊",
		"Your Weekly Summary",
		msg,
		"Continue Building",
		"http://localhost:5173/dashboard",
	)
	return s.Send(to, "Your Lovable weekly summary", body)
}
