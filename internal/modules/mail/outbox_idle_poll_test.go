package mail

import (
	"bytes"
	"context"
	"log"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// An idle worker must exit cleanly without flooding journalctl with
// GORM's 'record not found' error on each empty poll.
func TestMailOutboxIdleQueueIsNormalAndQuiet(t *testing.T) {
	db, sender, _, _, _, _, _ := mailOutboxFixture(t)
	var trace bytes.Buffer
	quiet := logger.New(log.New(&trace, "", 0), logger.Config{LogLevel: logger.Info})
	sender.db = db.Session(&gorm.Session{Logger: quiet})
	count, err := sender.RunDue(context.Background(), 20)
	require.NoError(t, err)
	require.Zero(t, count)
	require.NotContains(t, trace.String(), "record not found")
}
