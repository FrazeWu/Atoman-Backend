package books

import (
	"errors"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) GetBookReview(user authctx.CurrentUser, workID uuid.UUID) (BookReviewDTO, error) {
	if user.ID == uuid.Nil {
		return BookReviewDTO{}, apperr.Unauthorized("Login required")
	}
	if workID == uuid.Nil {
		return BookReviewDTO{}, apperr.BadRequest("validation.invalid_request", "work_id is required")
	}
	var review model.BookReview
	if err := s.db.Where("user_id = ? AND work_id = ?", user.ID, workID).First(&review).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return BookReviewDTO{}, apperr.NotFound("books.review_not_found", "Book review not found")
		}
		return BookReviewDTO{}, err
	}
	return buildBookReviewDTO(review), nil
}

func (s *Service) DeleteBookReview(user authctx.CurrentUser, workID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if workID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "work_id is required")
	}
	result := s.db.Unscoped().Where("user_id = ? AND work_id = ?", user.ID, workID).Delete(&model.BookReview{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperr.NotFound("books.review_not_found", "Book review not found")
	}
	return nil
}

func (s *Service) VoteBookEdit(user authctx.CurrentUser, editID uuid.UUID, value int) (BookEditDTO, error) {
	if user.ID == uuid.Nil {
		return BookEditDTO{}, apperr.Unauthorized("Login required")
	}
	if value != model.BookEditVoteUp && value != model.BookEditVoteDown {
		return BookEditDTO{}, apperr.BadRequest("validation.invalid_request", "vote value is invalid")
	}
	var edit model.BookEdit
	if err := s.db.Where("id = ?", editID).First(&edit).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return BookEditDTO{}, apperr.NotFound("books.edit_not_found", "Book edit not found")
		}
		return BookEditDTO{}, err
	}
	if edit.Status != model.BookEditStatusPending {
		return BookEditDTO{}, apperr.Conflict("books.vote_invalid_status", "Only pending edits can be voted on")
	}
	if edit.SubmittedBy == user.ID {
		return BookEditDTO{}, apperr.Forbidden("books.self_vote_forbidden", "You cannot vote on your own book edit")
	}
	var vote model.BookEditVote
	err := s.db.Where("edit_id = ? AND user_id = ?", editID, user.ID).First(&vote).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		vote = model.BookEditVote{EditID: editID, UserID: user.ID, Value: value}
		if err := s.db.Create(&vote).Error; err != nil {
			return BookEditDTO{}, err
		}
	} else if err != nil {
		return BookEditDTO{}, err
	} else if err := s.db.Model(&vote).Update("value", value).Error; err != nil {
		return BookEditDTO{}, err
	}
	return buildBookEditDTO(s.db, edit), nil
}

func bookEditVoteCounts(db *gorm.DB, editID uuid.UUID) (int64, int64) {
	if !db.Migrator().HasTable(&model.BookEditVote{}) {
		return 0, 0
	}
	var rows []struct {
		Value int   `gorm:"column:value"`
		Count int64 `gorm:"column:count"`
	}
	if db.Model(&model.BookEditVote{}).Select("value, COUNT(*) AS count").Where("edit_id = ?", editID).Group("value").Find(&rows).Error != nil {
		return 0, 0
	}
	var up, down int64
	for _, row := range rows {
		if row.Value == model.BookEditVoteUp {
			up = row.Count
		}
		if row.Value == model.BookEditVoteDown {
			down = row.Count
		}
	}
	return up, down
}
