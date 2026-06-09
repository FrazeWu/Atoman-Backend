package service

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
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
	// Generate verification code
	code, err := generateVerificationCode()
	if err != nil {
		return "", fmt.Errorf("failed to generate code: %w", err)
	}

	// Store code in database with 10 minute expiration
	// Use UPSERT to handle concurrent requests for the same email
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	verificationCode := model.EmailVerificationCode{
		Email:     email,
		Code:      code,
		ExpiresAt: expiresAt,
		Used:      false,
	}

	// Upsert: insert new record, or update existing unused code for this email
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "email"}},
		DoUpdates: clause.AssignmentColumns([]string{"code", "expires_at", "used"}),
	}).Create(&verificationCode).Error; err != nil {
		return "", fmt.Errorf("failed to store code: %w", err)
	}

	// Send email
	err = s.sendEmail(email, "Atoman邮箱验证", s.buildVerificationEmail(code))
	if err != nil {
		return "", fmt.Errorf("failed to send email: %w", err)
	}

	return code, nil
}

// VerifyCode verifies the code for the given email
func (s *EmailService) VerifyCode(email, code string) (bool, error) {
	// Find unused, non-expired verification code (use UTC for consistent comparison)
	var verificationCode model.EmailVerificationCode
	err := s.db.Where("email = ? AND code = ? AND used = ? AND expires_at > ?", email, code, false, time.Now().UTC()).
		First(&verificationCode).Error

	if err == gorm.ErrRecordNotFound {
		return false, nil // Code not found, expired, or already used
	}
	if err != nil {
		return false, err
	}

	// Mark code as used
	verificationCode.Used = true
	s.db.Save(&verificationCode)

	return true, nil
}

// sendEmail sends an email using Resend API
func (s *EmailService) sendEmail(to, subject, body string) error {
	if s.resendAPIKey == "" {
		// Development mode: just log the code
		log.Printf("[DEV MODE] Email to %s - Subject: %s", to, subject)
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

// buildVerificationEmail builds the HTML email content
func (s *EmailService) buildVerificationEmail(code string) string {
	return fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; line-height: 1.6; color: #1f2937; }
    .container { max-width: 600px; margin: 0 auto; padding: 2rem; }
    .header { background: #000; color: #fff; padding: 2rem; text-align: center; }
    .header h1 { margin: 0; font-size: 1.5rem; font-weight: 900; }
    .content { background: #fff; padding: 2rem; border: 2px solid #000; margin-top: -2px; }
    .code-box { background: #f9fafb; border: 2px solid #000; padding: 1.5rem; text-align: center; margin: 1.5rem 0; }
    .code { font-size: 2rem; font-weight: 900; letter-spacing: 0.5em; font-family: monospace; }
    .footer { margin-top: 2rem; padding-top: 1rem; border-top: 1px solid #e5e7eb; font-size: 0.875rem; color: #6b7280; }
  </style>
</head>
<body>
  <div class="container">
    <div class="header">
      <h1>Atoman</h1>
    </div>
    <div class="content">
      <h2>邮箱验证</h2>
      <p>感谢您注册Atoman！请使用以下验证码完成邮箱验证：</p>
      
      <div class="code-box">
        <div class="code">%s</div>
      </div>
      
      <p>验证码有效期为 <strong>10 分钟</strong>。请勿将此验证码分享给他人。</p>
      <p>如果您没有请求注册，请忽略此邮件。</p>
      
      <div class="footer">
        <p>此邮件由 Atoman系统自动发送，请勿回复。</p>
        <p>&copy; 2026 Atoman. All rights reserved.</p>
      </div>
    </div>
  </div>
</body>
</html>
`, code)
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
