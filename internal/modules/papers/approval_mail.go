package papers

import (
	"context"
	"errors"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ApprovalMailer interface {
	SendPapersApproval(context.Context, string, string) error
}

// Durable delivery uses the application as an outbox. A short database
// lease prevents concurrent senders, and the provider idempotency key is
// stable over the entire bounded retry period (under 24 hours).
func DeliverApprovalMail(ctx context.Context, db *gorm.DB, mailer ApprovalMailer) (bool, error) {
	if db == nil || mailer == nil {
		return false, errors.New("approval mail unavailable")
	}
	var a model.PaperAccessApplication
	now := time.Now().UTC()
	token := uuid.New()
	claimed := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Expired uncertain sends outside provider deduplication window are
		// marked failed, not sent a second time without operator investigation.
		if err := tx.Model(&model.PaperAccessApplication{}).Where("status='approved' AND notification_status IN ('pending','sending') AND notification_first_at IS NOT NULL AND notification_first_at < ?", now.Add(-23*time.Hour)).Updates(map[string]any{"notification_status": "failed", "notification_lease": nil, "notification_lease_until": nil}).Error; err != nil {
			return err
		}
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status='approved' AND ((notification_status='pending' AND notification_next_at<=?) OR (notification_status='sending' AND notification_lease_until<=?))", now, now).Order("notification_next_at ASC,id ASC").Take(&a).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if a.NotificationFirstAt == nil {
			a.NotificationFirstAt = &now
		}
		if a.NotificationAttempts >= 6 {
			return tx.Model(&a).Updates(map[string]any{"notification_status": "failed", "notification_lease": nil, "notification_lease_until": nil}).Error
		}
		if err := tx.Model(&a).Updates(map[string]any{"notification_status": "sending", "notification_first_at": a.NotificationFirstAt, "notification_attempts": a.NotificationAttempts + 1, "notification_lease": token, "notification_lease_until": now.Add(90 * time.Second)}).Error; err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if err != nil || !claimed {
		return false, err
	}
	status := "sent"
	var next, sent any
	var owner model.User
	var grant int64
	// Do not deliver to a stale/replaced email or a revoked account.
	check := db.WithContext(ctx).Where("uuid=? AND is_active=?", a.UserID, true).First(&owner).Error
	if check == nil {
		check = db.WithContext(ctx).Model(&model.PaperAccessGrant{}).Where("user_id=? AND status='active' AND (expires_at IS NULL OR expires_at>?)", a.UserID, now).Count(&grant).Error
	}
	if check != nil && !errors.Is(check, gorm.ErrRecordNotFound) {
		status = "pending"
		next = now.Add(time.Minute)
	} else if check != nil || owner.Email == "" || owner.Email != a.NotificationRecipient || grant != 1 {
		status = "failed"
	} else {
		bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = mailer.SendPapersApproval(bounded, owner.Email, "papers-approval-"+a.ID.String())
		cancel()
		if err == nil {
			sent = time.Now().UTC()
		} else if a.NotificationAttempts+1 >= 6 {
			status = "failed"
		} else {
			status = "pending"
			next = time.Now().UTC().Add(time.Duration(1<<a.NotificationAttempts) * time.Minute)
		}
	}
	finish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	update := db.WithContext(finish).Model(&model.PaperAccessApplication{}).Where("id=? AND notification_lease=? AND notification_status='sending'", a.ID, token).
		Updates(map[string]any{"notification_status": status, "notification_next_at": next, "notification_sent_at": sent, "notification_lease": nil, "notification_lease_until": nil})
	return true, update.Error
}

func StartApprovalMailWorker(ctx context.Context, db *gorm.DB, mailer ApprovalMailer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			// Limit each poll to 10 sends; no unbounded concurrency.
			for i := 0; i < 10; i++ {
				worked, err := DeliverApprovalMail(ctx, db, mailer)
				if err != nil || !worked {
					break
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}
