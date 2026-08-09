package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunMusicPartialDatesMigration(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.Artist{}, &model.ArtistMember{}, &model.Album{}, &model.Song{}); err != nil {
		return err
	}

	var artists []model.Artist
	if err := db.Select("id", "birth_date", "birth_date_precision", "active_start_date", "active_start_date_precision", "active_end_date", "active_end_date_precision").Find(&artists).Error; err != nil {
		return err
	}
	for _, artist := range artists {
		updates := map[string]any{}
		if artist.BirthDate != nil && artist.BirthDatePrecision == "" {
			updates["birth_date_precision"] = "day"
		}
		if !artist.ActiveStartDate.IsZero() && artist.ActiveStartDatePrecision == "" {
			updates["active_start_date_precision"] = "day"
		}
		if !artist.ActiveEndDate.IsZero() && artist.ActiveEndDatePrecision == "" {
			updates["active_end_date_precision"] = "day"
		}
		if len(updates) > 0 {
			if err := db.Model(&model.Artist{}).Where("id = ?", artist.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
	}

	var members []model.ArtistMember
	if err := db.Select("id", "join_date", "join_date_precision", "leave_date", "leave_date_precision").Find(&members).Error; err != nil {
		return err
	}
	for _, member := range members {
		updates := map[string]any{}
		if member.JoinDate != nil && member.JoinDatePrecision == "" {
			updates["join_date_precision"] = "day"
		}
		if member.LeaveDate != nil && member.LeaveDatePrecision == "" {
			updates["leave_date_precision"] = "day"
		}
		if len(updates) > 0 {
			if err := db.Model(&model.ArtistMember{}).Where("id = ?", member.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
	}

	var albums []model.Album
	if err := db.Select("id", "release_date", "release_date_precision").Find(&albums).Error; err != nil {
		return err
	}
	for _, album := range albums {
		if !album.ReleaseDate.IsZero() && album.ReleaseDatePrecision == "" {
			if err := db.Model(&model.Album{}).Where("id = ?", album.ID).Update("release_date_precision", "day").Error; err != nil {
				return err
			}
		}
	}

	var songs []model.Song
	if err := db.Select("id", "release_date", "release_date_precision").Find(&songs).Error; err != nil {
		return err
	}
	for _, song := range songs {
		if !song.ReleaseDate.IsZero() && song.ReleaseDatePrecision == "" {
			if err := db.Model(&model.Song{}).Where("id = ?", song.ID).Update("release_date_precision", "day").Error; err != nil {
				return err
			}
		}
	}
	return nil
}
