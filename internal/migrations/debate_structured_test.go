package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRunDebateStructuredMigrationIsIdempotentAndEnforcesOpenChallengePartialUniqueIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Debate{},
		&model.DebateEntry{},
		&model.DebateEvidenceVote{},
		&model.DebateChallenge{},
		&model.DebateChallengeVote{},
	)

	require.NoError(t, RunDebateStructuredMigration(db))
	require.NoError(t, RunDebateStructuredMigration(db))

	user := model.User{
		UUID:     uuid.New(),
		Username: "structured-mig",
		Email:    "structured-mig@example.com",
		Password: "hash",
		IsActive: true,
	}
	require.NoError(t, db.Create(&user).Error)
	debate := model.Debate{UserID: user.UUID, Title: "Structured Migration", Status: model.DebateStatusActive}
	require.NoError(t, db.Create(&debate).Error)
	targetID := uuid.New()

	// Previously dismissed or upheld challenges on the same target must allow a new open challenge.
	dismissed := model.DebateChallenge{
		DebateID:   debate.ID,
		TargetType: model.DebateChallengeTargetEntry,
		TargetID:   targetID,
		CreatedBy:  user.UUID,
		ReasonCode: model.DebateChallengeReasonMismatch,
		Rationale:  "old dismissed",
		Status:     model.DebateChallengeStatusDismissed,
	}
	require.NoError(t, db.Create(&dismissed).Error)

	openFirst := model.DebateChallenge{
		DebateID:   debate.ID,
		TargetType: model.DebateChallengeTargetEntry,
		TargetID:   targetID,
		CreatedBy:  user.UUID,
		ReasonCode: model.DebateChallengeReasonOutdated,
		Rationale:  "first open",
		Status:     model.DebateChallengeStatusOpen,
	}
	require.NoError(t, db.Create(&openFirst).Error)

	// A second simultaneous open challenge on the same target violates uq_debate_challenge_open_target.
	openDuplicate := model.DebateChallenge{
		DebateID:   debate.ID,
		TargetType: model.DebateChallengeTargetEntry,
		TargetID:   targetID,
		CreatedBy:  user.UUID,
		ReasonCode: model.DebateChallengeReasonIrrelevant,
		Rationale:  "second open",
		Status:     model.DebateChallengeStatusOpen,
	}
	err := db.Create(&openDuplicate).Error
	require.Error(t, err)
}
