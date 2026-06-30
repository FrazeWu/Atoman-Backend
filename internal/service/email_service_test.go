package service

import (
	"bytes"
	"log"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/google/uuid"
)

func captureEmailServiceLogs(t *testing.T, fn func()) string {
	t.Helper()

	var buf bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(oldOutput)
	})

	fn()

	return buf.String()
}

func TestSendVerificationCodeWithoutEmailConfigDoesNotLogSecrets(t *testing.T) {
	t.Setenv("RESEND_API_KEY", "")
	t.Setenv("FROM_EMAIL", "")
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.EmailVerificationCode{})

	email := "debug-leak@example.com"
	service := NewEmailServiceWithoutRedis(db)

	var code string
	logs := captureEmailServiceLogs(t, func() {
		var err error
		code, err = service.SendVerificationCode(email)
		if err != nil {
			t.Fatalf("send verification code: %v", err)
		}
	})

	if code == "" {
		t.Fatal("expected verification code")
	}
	if bytes.Contains([]byte(logs), []byte(code)) {
		t.Fatalf("expected logs not to contain verification code %q, got %q", code, logs)
	}
	if bytes.Contains([]byte(logs), []byte(email)) {
		t.Fatalf("expected logs not to contain full email %q, got %q", email, logs)
	}
}

func TestVerifyCodeConsumesCodeAtomically(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.EmailVerificationCode{})

	email := uuid.NewString() + "@example.com"
	code := "123456"
	if err := db.Create(&model.EmailVerificationCode{
		Email:     email,
		Code:      code,
		ExpiresAt: time.Now().UTC().Add(time.Minute),
		Used:      false,
	}).Error; err != nil {
		t.Fatalf("seed verification code: %v", err)
	}

	service := NewEmailServiceWithoutRedis(db)

	const attempts = 2
	results := make(chan bool, attempts)
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	wg.Add(attempts)

	for range attempts {
		go func() {
			defer wg.Done()
			valid, err := service.VerifyCode(email, code)
			results <- valid
			errs <- err
		}()
	}

	wg.Wait()
	close(results)
	close(errs)

	var successCount int
	for valid := range results {
		if valid {
			successCount++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("verify code returned error: %v", err)
		}
	}

	if successCount != 1 {
		t.Fatalf("expected exactly one successful verification, got %d", successCount)
	}

	var stored model.EmailVerificationCode
	if err := db.Where("email = ? AND code = ?", email, code).First(&stored).Error; err != nil {
		t.Fatalf("load verification code: %v", err)
	}
	if !stored.Used {
		t.Fatal("expected verification code to be marked used")
	}
}
