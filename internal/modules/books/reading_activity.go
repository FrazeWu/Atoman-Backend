package books

import (
	"context"
	"sort"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const bookReadingHeartbeatMaxGap = 30 * time.Second

type BookReadingHeartbeatInput struct {
	Sequence int64 `json:"sequence"`
	Visible  bool  `json:"visible"`
	Active   bool  `json:"active"`
}

type BookReadingHeartbeatResult struct {
	LastSequence    int64 `json:"last_sequence"`
	CreditedSeconds int64 `json:"credited_seconds"`
	Ignored         bool  `json:"ignored"`
}

type BookReadingDayDTO struct {
	Day     string `json:"day"`
	Seconds int64  `json:"seconds"`
}
type BookReadingWorkDTO struct {
	WorkID  uuid.UUID `json:"work_id"`
	Title   string    `json:"title"`
	Seconds int64     `json:"seconds"`
}
type BookReadingStatistics struct {
	Timezone     string               `json:"timezone"`
	From         string               `json:"from"`
	To           string               `json:"to"`
	TotalSeconds int64                `json:"total_seconds"`
	Days         []BookReadingDayDTO  `json:"days"`
	Works        []BookReadingWorkDTO `json:"works"`
}

func (s *Service) RecordBookReadingHeartbeat(ctx context.Context, user authctx.CurrentUser, sessionID uuid.UUID, input BookReadingHeartbeatInput) (BookReadingHeartbeatResult, error) {
	// Never accept client timestamps or claimed durations. Production time is
	// sampled after locks; fixed server time is only supplied by package tests.
	return s.recordBookReadingHeartbeatAt(ctx, user, sessionID, input, time.Time{})
}

func (s *Service) recordBookReadingHeartbeatAt(ctx context.Context, user authctx.CurrentUser, sessionID uuid.UUID, input BookReadingHeartbeatInput, now time.Time) (BookReadingHeartbeatResult, error) {
	if user.ID == uuid.Nil {
		return BookReadingHeartbeatResult{}, apperr.Unauthorized("Login required")
	}
	if sessionID == uuid.Nil || input.Sequence < 1 {
		return BookReadingHeartbeatResult{}, apperr.BadRequest("books.invalid_reading_heartbeat", "A reading session and positive sequence are required")
	}
	var result BookReadingHeartbeatResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session model.BookReadingSession
		if err := tx.Where("id = ? AND user_id = ?", sessionID, user.ID).First(&session).Error; err != nil {
			return privateBookReadingSessionError(err)
		}
		clock := model.BookReaderClock{UserID: user.ID, SessionID: session.ID, RevisionID: session.RevisionID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&clock).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&clock, "user_id = ?", user.ID).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, "id = ? AND user_id = ?", sessionID, user.ID).Error; err != nil {
			return privateBookReadingSessionError(err)
		}
		result.LastSequence = session.HeartbeatSequence
		if input.Sequence <= session.HeartbeatSequence {
			result.Ignored = true
			return nil
		}
		if err := tx.Model(&session).Update("heartbeat_sequence", input.Sequence).Error; err != nil {
			return err
		}
		result.LastSequence = input.Sequence
		if now.IsZero() {
			now = time.Now().UTC()
		} else {
			now = now.UTC()
		}
		active := input.Visible && input.Active
		// A hidden/background device cannot stop the foreground device's clock.
		if (!active && clock.SessionID != session.ID) || now.Before(clock.LastSeenAt) {
			return nil
		}
		gap := now.Sub(clock.LastSeenAt)
		if active && clock.Active && clock.SessionID == session.ID && gap > 0 && gap <= bookReadingHeartbeatMaxGap {
			result.CreditedSeconds = int64(gap / time.Second)
			if err := creditBookReadingDays(tx, user.ID, session.RevisionID, now, result.CreditedSeconds); err != nil {
				return err
			}
		}
		return tx.Model(&clock).Updates(map[string]any{"session_id": session.ID, "revision_id": session.RevisionID, "last_seen_at": now, "active": active}).Error
	})
	return result, err
}

func creditBookReadingDays(tx *gorm.DB, userID, revisionID uuid.UUID, end time.Time, seconds int64) error {
	end = end.UTC().Truncate(time.Second)
	start := end.Add(-time.Duration(seconds) * time.Second)
	for remaining := seconds; remaining > 0; {
		midnight := time.Date(start.Year(), start.Month(), start.Day()+1, 0, 0, 0, 0, time.UTC)
		part := remaining
		if until := int64(midnight.Sub(start) / time.Second); until < part {
			part = until
		}
		day := model.BookReadingDaily{UserID: userID, RevisionID: revisionID, Day: start.Format("2006-01-02"), Seconds: part}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "revision_id"}, {Name: "day"}}, DoUpdates: clause.Assignments(map[string]any{"seconds": gorm.Expr("book_reading_daily.seconds + EXCLUDED.seconds")})}).Create(&day).Error; err != nil {
			return err
		}
		remaining -= part
		start = start.Add(time.Duration(part) * time.Second)
	}
	return nil
}

func (s *Service) GetBookReadingStatistics(ctx context.Context, user authctx.CurrentUser, from, to string) (BookReadingStatistics, error) {
	if user.ID == uuid.Nil {
		return BookReadingStatistics{}, apperr.Unauthorized("Login required")
	}
	today := time.Now().UTC()
	if to == "" {
		to = today.Format("2006-01-02")
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil {
		return BookReadingStatistics{}, apperr.BadRequest("books.invalid_statistics_range", "Use YYYY-MM-DD dates")
	}
	if from == "" {
		from = end.AddDate(0, 0, -29).Format("2006-01-02")
	}
	start, err := time.Parse("2006-01-02", from)
	if err != nil || end.Before(start) || end.Sub(start) > 365*24*time.Hour {
		return BookReadingStatistics{}, apperr.BadRequest("books.invalid_statistics_range", "Use an inclusive range of at most 366 days")
	}
	result := BookReadingStatistics{Timezone: "UTC", From: from, To: to, Days: []BookReadingDayDTO{}, Works: []BookReadingWorkDTO{}}
	var rows []struct {
		Day     string
		WorkID  uuid.UUID
		Title   string
		Seconds int64
	}
	err = s.db.WithContext(ctx).Model(&model.BookReadingDaily{}).
		Select("book_reading_daily.day, a.work_id, w.title, SUM(book_reading_daily.seconds) AS seconds").
		Joins("JOIN book_asset_revisions r ON r.id = book_reading_daily.revision_id").
		Joins("JOIN book_assets a ON a.id = r.asset_id").Joins("JOIN book_works w ON w.id = a.work_id").
		Where("book_reading_daily.user_id = ? AND book_reading_daily.day >= ? AND book_reading_daily.day <= ?", user.ID, from, to).
		Group("book_reading_daily.day, a.work_id, w.title").Order("book_reading_daily.day ASC, a.work_id ASC").Scan(&rows).Error
	if err != nil {
		return result, err
	}
	workIndices := map[uuid.UUID]int{}
	for _, row := range rows {
		result.TotalSeconds += row.Seconds
		if len(result.Days) == 0 || result.Days[len(result.Days)-1].Day != row.Day {
			result.Days = append(result.Days, BookReadingDayDTO{Day: row.Day})
		}
		result.Days[len(result.Days)-1].Seconds += row.Seconds
		index, ok := workIndices[row.WorkID]
		if !ok {
			index = len(result.Works)
			workIndices[row.WorkID] = index
			result.Works = append(result.Works, BookReadingWorkDTO{WorkID: row.WorkID, Title: row.Title})
		}
		result.Works[index].Seconds += row.Seconds
	}
	sort.Slice(result.Works, func(i, j int) bool {
		if result.Works[i].Seconds != result.Works[j].Seconds {
			return result.Works[i].Seconds > result.Works[j].Seconds
		}
		return result.Works[i].WorkID.String() < result.Works[j].WorkID.String()
	})
	return result, nil
}
