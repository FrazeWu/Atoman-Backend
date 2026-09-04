package migrations

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

type ratingConstraint struct {
	table      string
	constraint string
	legacyFive bool
}

// RunRatingTenPointMigration preserves old star values while moving every
// persisted rating to the shared 1-10 half-star unit.
func RunRatingTenPointMigration(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	tables := []ratingConstraint{
		{table: "song_ratings", constraint: "chk_song_ratings_score", legacyFive: true},
		{table: "album_ratings", constraint: "chk_album_ratings_score", legacyFive: true},
		{table: "book_ratings", constraint: "chk_book_ratings_score", legacyFive: true},
		{table: "post_ratings", constraint: "chk_post_ratings_score"},
		{table: "feed_item_ratings", constraint: "chk_feed_item_ratings_score"},
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, item := range tables {
			if !tx.Migrator().HasTable(item.table) {
				continue
			}
			definition, err := postgresCheckConstraintDefinition(tx, item.table, item.constraint)
			if err != nil {
				return err
			}
			legacy := item.legacyFive && isFivePointConstraint(definition)
			if legacy {
				if err := tx.Exec(fmt.Sprintf(`ALTER TABLE "%s" DROP CONSTRAINT "%s"`, item.table, item.constraint)).Error; err != nil {
					return fmt.Errorf("drop legacy %s constraint: %w", item.table, err)
				}
				if err := tx.Exec(fmt.Sprintf(`UPDATE "%s" SET score = score * 2`, item.table)).Error; err != nil {
					return fmt.Errorf("convert %s scores: %w", item.table, err)
				}
				definition = ""
			}
			if definition == "" {
				statement := fmt.Sprintf(`ALTER TABLE "%s" ADD CONSTRAINT "%s" CHECK (score BETWEEN 1 AND 10)`, item.table, item.constraint)
				if err := tx.Exec(statement).Error; err != nil {
					return fmt.Errorf("add ten-point %s constraint: %w", item.table, err)
				}
			}
		}
		return nil
	})
}

func postgresCheckConstraintDefinition(db *gorm.DB, table, constraint string) (string, error) {
	var row struct {
		Definition string `gorm:"column:definition"`
	}
	result := db.Raw(`SELECT pg_get_constraintdef(c.oid) AS definition
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = current_schema() AND t.relname = ? AND c.conname = ?`, table, constraint).Scan(&row)
	if result.Error != nil {
		return "", result.Error
	}
	return row.Definition, nil
}

func isFivePointConstraint(definition string) bool {
	normalized := strings.Join(strings.Fields(strings.ToUpper(definition)), " ")
	return strings.Contains(normalized, "BETWEEN 1 AND 5") || strings.Contains(normalized, "<= 5")
}
