package email

import (
	"fmt"
	"net/smtp"
	"os"
)

// Send sends a plain text email via Gmail SMTP using an app password.
// Requires SMTP_HOST, SMTP_PORT, SMTP_USER, SMTP_PASS environment variables.
func Send(toEmail, subject, body string) error {
	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")

	if host == "" || port == "" || user == "" || pass == "" {
		return fmt.Errorf("SMTP environment variables not fully configured")
	}

	addr := fmt.Sprintf("%s:%s", host, port)
	auth := smtp.PlainAuth("", user, pass, host)

	msg := fmt.Sprintf("From: Escrowd <%s>\r\n"+
		"To: %s\r\n"+
		"Subject: %s\r\n"+
		"MIME-Version: 1.0\r\n"+
		"Content-Type: text/plain; charset=\"UTF-8\"\r\n"+
		"\r\n"+
		"%s\r\n", user, toEmail, subject, body)

	if err := smtp.SendMail(addr, auth, user, []string{toEmail}, []byte(msg)); err != nil {
		return fmt.Errorf("could not send email: %w", err)
	}
	return nil
}

// SendVerificationEmail sends the account verification link to a new user.
func SendVerificationEmail(toEmail, username, verifyURL string) error {
	subject := "Verify your Escrowd account"
	body := fmt.Sprintf(`Hi %s,

Welcome to Escrowd — secure escrow for trading with strangers, backed by the Stellar blockchain.

Please verify your email address by clicking the link below:

%s

This link expires in 24 hours. If you did not create this account, ignore this email.

— Escrowd Team
`, username, verifyURL)
	return Send(toEmail, subject, body)
}

// SendDealInviteEmail notifies a counterparty they've been invited to an escrow deal.
func SendDealInviteEmail(toEmail, inviterName, dealTitle, inviteURL string) error {
	subject := fmt.Sprintf("%s invited you to an Escrowd deal", inviterName)
	body := fmt.Sprintf(`Hi,

%s has created an escrow deal with you on Escrowd:

"%s"

Click the link below to view and accept the deal:

%s

If you don't recognise this, you can safely ignore this email.

— Escrowd Team
`, inviterName, dealTitle, inviteURL)
	return Send(toEmail, subject, body)
}
