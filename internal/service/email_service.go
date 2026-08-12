package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"atoman/internal/model"
)

// EmailService handles email sending operations
type EmailService struct {
	db           *gorm.DB
	resendAPIKey string
	fromEmail    string
}

const (
	VerificationPurposeRegistration  = "registration"
	VerificationPurposePasswordReset = "password_reset"
	VerificationPurposeOAuthEmail    = "oauth_email"
	VerificationPurposeEmailChange   = "email_change"
)

// NewEmailService creates a new email service instance
func NewEmailService(redisClient interface{}, db *gorm.DB) *EmailService {
	// redisClient parameter kept for compatibility but not used
	return &EmailService{
		db:           db,
		resendAPIKey: os.Getenv("RESEND_API_KEY"),
		fromEmail:    os.Getenv("FROM_EMAIL"),
	}
}

// NewEmailServiceWithoutRedis creates a new email service instance without Redis
func NewEmailServiceWithoutRedis(db *gorm.DB) *EmailService {
	return &EmailService{
		db:           db,
		resendAPIKey: os.Getenv("RESEND_API_KEY"),
		fromEmail:    os.Getenv("FROM_EMAIL"),
	}
}

// generateVerificationCode generates a random 6-digit verification code
func generateVerificationCode() (string, error) {
	charset := "0123456789"
	code := ""
	for i := 0; i < 6; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		code += string(charset[n.Int64()])
	}
	return code, nil
}

// SendVerificationCode sends a verification code to the given email
func (s *EmailService) SendVerificationCode(email string) (string, error) {
	return s.SendVerificationCodeForPurpose(email, VerificationPurposeRegistration)
}

func (s *EmailService) SendVerificationCodeForPurpose(email, purpose string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	purpose = strings.TrimSpace(purpose)
	// Generate verification code
	code, err := generateVerificationCode()
	if err != nil {
		return "", fmt.Errorf("failed to generate code: %w", err)
	}
	codeHash, err := verificationCodeDigest(email, purpose, code)
	if err != nil {
		return "", err
	}

	// Store code in database with 10 minute expiration
	// Use UPSERT to handle concurrent requests for the same email
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	verificationCode := model.EmailVerificationCode{
		Email:          email,
		Purpose:        purpose,
		CodeHash:       codeHash,
		FailedAttempts: 0,
		ExpiresAt:      expiresAt,
		Used:           false,
	}

	// Upsert: insert new record, or update existing unused code for this email
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "email"}, {Name: "purpose"}},
		DoUpdates: clause.AssignmentColumns([]string{"code", "failed_attempts", "expires_at", "used"}),
	}).Create(&verificationCode).Error; err != nil {
		return "", fmt.Errorf("failed to store code: %w", err)
	}

	// Send email
	subject := "Atoman邮箱验证"
	if purpose == VerificationPurposePasswordReset {
		subject = "Atoman密码重置"
	} else if purpose == VerificationPurposeOAuthEmail {
		subject = "Atoman邮箱确认"
	} else if purpose == VerificationPurposeEmailChange {
		subject = "Atoman邮箱变更确认"
	}
	err = s.sendEmail(email, subject, s.buildVerificationEmail(code, purpose))
	if err != nil {
		return "", fmt.Errorf("failed to send email: %w", err)
	}

	return code, nil
}

// VerifyCode verifies the code for the given email
func (s *EmailService) VerifyCode(email, code string) (bool, error) {
	return s.VerifyCodeForPurpose(email, code, VerificationPurposeRegistration)
}

func (s *EmailService) VerifyCodeForPurpose(email, code, purpose string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	purpose = strings.TrimSpace(purpose)
	codeHash, err := verificationCodeDigest(email, purpose, strings.TrimSpace(code))
	if err != nil {
		return false, err
	}
	// Atomically consume an unused, non-expired verification code.
	// The conditional update closes the race where two requests could both
	// read the same row before either one marks it as used.
	now := time.Now().UTC()
	result := s.db.Model(&model.EmailVerificationCode{}).
		Where("email = ? AND code = ? AND purpose = ? AND used = ? AND failed_attempts < ? AND expires_at > ?", email, codeHash, purpose, false, 5, now).
		Update("used", true)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		failed := s.db.Model(&model.EmailVerificationCode{}).
			Where("email = ? AND purpose = ? AND used = ? AND failed_attempts < ? AND expires_at > ?", email, purpose, false, 5, now).
			UpdateColumn("failed_attempts", gorm.Expr("failed_attempts + 1"))
		if failed.Error != nil {
			return false, failed.Error
		}
		return false, nil
	}

	return true, nil
}

func verificationCodeDigest(email, purpose, code string) (string, error) {
	secret := strings.TrimSpace(os.Getenv("AUTH_CODE_SECRET"))
	if secret == "" {
		if os.Getenv("ENV") == "production" {
			return "", fmt.Errorf("AUTH_CODE_SECRET is not configured")
		}
		secret = "atoman-development-verification-secret"
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(email))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(code))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// sendEmail sends an email using Resend API
func (s *EmailService) sendEmail(to, subject, body string) error {
	if s.resendAPIKey == "" {
		log.Printf("[DEV MODE] Verification email skipped; recipient=%s", maskEmailForLog(to))
		return nil
	}

	// Resend API endpoint
	url := "https://api.resend.com/emails"

	// Request payload
	payload := map[string]interface{}{
		"from":    s.fromEmail,
		"to":      []string{to},
		"subject": subject,
		"html":    body,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.resendAPIKey)

	// Send request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		var respErr map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&respErr)
		return fmt.Errorf("resend API error (%d): %v", resp.StatusCode, respErr)
	}

	return nil
}

func maskEmailForLog(email string) string {
	at := -1
	for i, ch := range email {
		if ch == '@' {
			at = i
			break
		}
	}
	if at <= 0 || at == len(email)-1 {
		return "[redacted]"
	}
	return email[:1] + "***" + email[at:]
}

// buildVerificationEmail builds the HTML email content

func (s *EmailService) buildVerificationEmail(code, purpose string) string {
	title := "邮箱验证"
	description := "请使用以下验证码完成邮箱验证："
	ignoreMessage := "如果您没有请求注册，请忽略此邮件。"
	if purpose == VerificationPurposePasswordReset {
		title = "重置密码"
		description = "请使用以下验证码重置密码："
		ignoreMessage = "如果您没有请求重置密码，请忽略此邮件。"
	}
	return fmt.Sprintf(`
<!DOCTYPE html>
<html lang="zh-CN">
<head>
	  <meta charset="UTF-8">
	  <meta name="viewport" content="width=device-width, initial-scale=1.0">
	  <style>
	    body { margin: 0; padding: 0; background: #f8fafc; color: #0f172a; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; line-height: 1.6; }
	    .email-shell { width: 100%%; padding: 40px 16px; }
	    .email-card { width: 100%%; max-width: 560px; margin: 0 auto; background: #ffffff; border: 1px solid #e2e8f0; border-radius: 4px; box-shadow: 0 12px 30px rgba(15, 23, 42, 0.08); }
	    .brand-bar { padding: 24px 32px; border-bottom: 1px solid #e2e8f0; }
	    .brand-mark { display: inline-block; width: 28px; height: 28px; margin-right: 10px; border-radius: 4px; background: #2563eb; color: #ffffff; font-size: 16px; font-weight: 700; line-height: 28px; text-align: center; vertical-align: middle; }
	    .brand-name { color: #0f172a; font-size: 18px; font-weight: 600; line-height: 28px; vertical-align: middle; }
	    .content { padding: 36px 32px 32px; }
	    .eyebrow { margin: 0 0 8px; color: #2563eb; font-size: 12px; font-weight: 600; letter-spacing: 0; text-transform: uppercase; }
	    .title { margin: 0; color: #0f172a; font-size: 24px; font-weight: 600; line-height: 1.3; }
	    .description { margin: 16px 0 0; color: #334155; font-size: 16px; }
	    .code-box { margin: 28px 0 20px; padding: 22px 16px; border: 1px solid #bfdbfe; border-radius: 4px; background: #eff6ff; text-align: center; }
	    .code-label { margin: 0 0 8px; color: #64748b; font-size: 12px; }
	    .code { color: #1d4ed8; font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', monospace; font-size: 32px; font-weight: 600; letter-spacing: 6px; line-height: 1.2; }
	    .notice { margin: 0; color: #64748b; font-size: 14px; }
	    .footer { margin-top: 32px; padding-top: 20px; border-top: 1px solid #e2e8f0; color: #94a3b8; font-size: 12px; }
	    .footer p { margin: 4px 0; }
	    @media only screen and (max-width: 600px) {
	      .email-shell { padding: 20px 12px; }
	      .brand-bar, .content { padding-left: 24px; padding-right: 24px; }
	      .code { font-size: 28px; letter-spacing: 4px; }
	    }
	  </style>
</head>
<body>
	  <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%%">
	    <tr>
	      <td class="email-shell">
	        <table role="presentation" cellpadding="0" cellspacing="0" border="0" class="email-card">
	          <tr>
	            <td class="brand-bar">
	              <span class="brand-mark">A</span><span class="brand-name">Atoman</span>
	            </td>
	          </tr>
	          <tr>
	            <td class="content">
	              <p class="eyebrow">Atoman 账户</p>
	              <h1 class="title">%s</h1>
	              <p class="description">%s</p>

	              <div class="code-box">
	                <p class="code-label">验证码</p>
	                <div class="code">%s</div>
	              </div>

	              <p class="notice">验证码有效期为 <strong>10 分钟</strong>。请勿将此验证码分享给他人。</p>
	              <p class="notice" style="margin-top: 8px;">%s</p>

	              <div class="footer">
	                <p>此邮件由 Atoman 自动发送，请勿直接回复。</p>
	                <p>&copy; 2026 Atoman. All rights reserved.</p>
	              </div>
	            </td>
	          </tr>
	        </table>
	      </td>
	    </tr>
	  </table>
</body>
</html>
	`, title, description, code, ignoreMessage)
}

// Resend Setup:
// 1. Sign up at https://resend.com
// 2. Get your API key from https://resend.com/api-keys
// 3. Add verified domain or use onboarding@resend.dev for testing
// 4. Configure environment variables:
//    RESEND_API_KEY=re_xxxxxxxxxxxxxxxxxxxxx
//    FROM_EMAIL=your-domain@resend.dev (or noreply@yourdomain.com)
//
// Free tier: 3,000 emails/month, 100 emails/day
