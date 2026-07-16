package timeline

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseDateTimeSupportsBCEAndExistingPrecision(t *testing.T) {
	bce, err := ParseDateTime("-0044-03-15")
	require.NoError(t, err)
	require.Equal(t, -44, bce.Year())
	require.Equal(t, 3, int(bce.Month()))
	require.Equal(t, 15, bce.Day())
	minute, err := ParseDateTime("2024-05-01T12:34")
	require.NoError(t, err)
	require.Equal(t, 34, minute.Minute())
}
