package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The integrity migration is non-destructive: legacy malformed editions
// remain until a human verifies their bibliographic identity.
func TestBooksCatalogIntegrityMigration(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{})
	old := model.BookEdition{Title: "旧记录", WorkID: uuid.New(), LifecycleStatus: "active"}
	require.NoError(t, db.Create(&old).Error) // Legacy state before new constraints.

	require.NoError(t, RunBooksCatalogIntegrityMigration(db))
	require.NoError(t, RunBooksCatalogIntegrityMigration(db))
	var retained model.BookEdition
	require.NoError(t, db.First(&retained, "id = ?", old.ID).Error)

	work := model.BookWork{Title: "原作", LifecycleStatus: "active"}
	require.NoError(t, db.Create(&work).Error)
	person := model.BookPerson{Name: "作者", LifecycleStatus: "active"}
	require.NoError(t, db.Create(&person).Error)

	require.Error(t, db.Create(&model.BookEdition{WorkID: uuid.New(), Language: "zh", Publisher: "社", LifecycleStatus: "active"}).Error)
	require.Error(t, db.Create(&model.BookEdition{WorkID: work.ID, LifecycleStatus: "active"}).Error)
	edition := model.BookEdition{WorkID: work.ID, Language: "zh", Publisher: "社", LifecycleStatus: "active"}
	require.NoError(t, db.Create(&edition).Error)

	require.Error(t, db.Create(&model.BookContribution{PersonID: person.ID, Role: "author"}).Error)
	require.Error(t, db.Create(&model.BookContribution{WorkID: &work.ID, EditionID: &edition.ID, PersonID: person.ID, Role: "author"}).Error)
	require.Error(t, db.Create(&model.BookContribution{WorkID: &work.ID, PersonID: uuid.New(), Role: "author"}).Error)
	require.Error(t, db.Create(&model.BookContribution{EditionID: &uuid.UUID{}, PersonID: person.ID, Role: "translator"}).Error)

	author := model.BookContribution{WorkID: &work.ID, PersonID: person.ID, Role: "author"}
	require.NoError(t, db.Create(&author).Error)
	require.Error(t, db.Create(&model.BookContribution{WorkID: &work.ID, PersonID: person.ID, Role: "author"}).Error)

	translator := model.BookContribution{EditionID: &edition.ID, PersonID: person.ID, Role: "translator"}
	require.NoError(t, db.Create(&translator).Error)
	require.Error(t, db.Create(&model.BookContribution{EditionID: &edition.ID, PersonID: person.ID, Role: "translator"}).Error)
}
