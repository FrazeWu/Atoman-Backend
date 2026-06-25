package migrations

import (
	"testing"

	"atoman/internal/testdb"
)

func TestDeduplicateSubscriptionsSkipsMissingTable(t *testing.T) {
	db := testdb.Open(t)

	if err := DeduplicateSubscriptions(db); err != nil {
		t.Fatalf("expected missing subscriptions table to be skipped, got %v", err)
	}
}
