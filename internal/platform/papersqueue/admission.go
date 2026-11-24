package papersqueue

import (
	"context"
	"errors"

	"atoman/internal/model"
	"atoman/internal/platform/papersassets"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var ErrQuota = errors.New("papers queue: compile admission quota exceeded")

// Conservative single-node initial release limits. All active states reserve
// their maximum output size. Object-store quota is a separate production gate:
// DB accounting cannot include abandoned/unreferenced immutable objects.
const (
	maxOwnerActive          = 2
	maxGlobalActive         = 16
	maxOwnerHourly          = 30
	maxGlobalHourly         = 60
	maxOwnerPDFBytes  int64 = 2 << 30
	maxGlobalPDFBytes int64 = 8 << 30
)

type admissionStats struct {
	OwnerActive    int64
	GlobalActive   int64
	OwnerHourly    int64
	GlobalHourly   int64
	OwnerPDFBytes  int64
	GlobalPDFBytes int64
}

func checkAdmission(tx *gorm.DB, owner uuid.UUID) error {
	var slot model.PaperCompileSlot
	if err := tx.Where("id=1").Take(&slot).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotReady
		}
		return err
	}
	if slot.Blocked {
		return ErrBlocked
	}
	var stats admissionStats
	err := tx.Raw(`
SELECT
 COUNT(*) FILTER (WHERE owner_id = ? AND status IN ('queued','running','recovering')) AS owner_active,
 COUNT(*) FILTER (WHERE status IN ('queued','running','recovering')) AS global_active,
 COUNT(*) FILTER (WHERE owner_id = ? AND created_at >= CURRENT_TIMESTAMP - INTERVAL '1 hour') AS owner_hourly,
 COUNT(*) FILTER (WHERE created_at >= CURRENT_TIMESTAMP - INTERVAL '1 hour') AS global_hourly,
 COALESCE(SUM(pdf_size) FILTER (WHERE owner_id = ? AND status='succeeded'),0) AS owner_pdf_bytes,
 COALESCE(SUM(pdf_size) FILTER (WHERE status='succeeded'),0) AS global_pdf_bytes
FROM paper_compile_jobs
`, owner, owner, owner).Scan(&stats).Error
	if err != nil {
		return err
	}
	if stats.OwnerActive >= maxOwnerActive || stats.GlobalActive >= maxGlobalActive ||
		stats.OwnerHourly >= maxOwnerHourly || stats.GlobalHourly >= maxGlobalHourly ||
		stats.OwnerPDFBytes > maxOwnerPDFBytes-(stats.OwnerActive+1)*papersassets.MaxPDFBytes ||
		stats.GlobalPDFBytes > maxGlobalPDFBytes-(stats.GlobalActive+1)*papersassets.MaxPDFBytes {
		return ErrQuota
	}
	return nil
}

// CheckAdmission is a cheap pre-publication rejection, not an authorization
// grant or a reservation. Submit rechecks atomically under the global slot
// lock after ownership, revision and idempotency have been established.
func (q *Queue) CheckAdmission(ctx context.Context, owner uuid.UUID) error {
	if q == nil || q.db == nil {
		return ErrNotReady
	}
	if ctx == nil || owner == uuid.Nil {
		return ErrInvalid
	}
	if !q.requirePDF {
		return nil
	}
	return checkAdmission(q.db.WithContext(ctx), owner)
}
