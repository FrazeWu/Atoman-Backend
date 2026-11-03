package feed

import (
	"context"
	"math/big"
)

// A per-source checkpoint is mandatory before attempting an incremental
// repair. The summed revision alone proves that something changed, but not
// which feed source or which exact transaction was responsible.
func (r *Repo) dedupSourceCheckpoint(ctx context.Context) (map[string]int64, string, error) {
	var ledgers []feedDedupSourceRevision
	if err := r.db.WithContext(ctx).Find(&ledgers).Error; err != nil {
		return nil, "", err
	}
	versions := make(map[string]int64, len(ledgers))
	var sum big.Int
	for _, ledger := range ledgers {
		versions[ledger.FeedSourceID.String()] = ledger.Revision
		sum.Add(&sum, big.NewInt(ledger.Revision))
	}
	return versions, sum.String(), nil
}
