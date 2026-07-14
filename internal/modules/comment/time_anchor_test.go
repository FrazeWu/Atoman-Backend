package comment

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func anchorSeconds(anchors []TimeAnchor) []int {
	seconds := make([]int, len(anchors))
	for i, anchor := range anchors {
		seconds[i] = anchor.Seconds
	}
	return seconds
}

func TestParseTimeAnchorsFindsEveryValidToken(t *testing.T) {
	got := ParseTimeAnchors("开头 1:24，中段 01:02:03，错误 2:99", 4000)
	require.Equal(t, []int{84, 3723}, anchorSeconds(got))
}

func TestParseTimeAnchorsReturnsRuneOffsetsAndOccurrences(t *testing.T) {
	got := ParseTimeAnchors("你 1:02，再看 1:02", 0)
	require.Equal(t, []TimeAnchor{
		{Start: 2, End: 6, Seconds: 62},
		{Start: 10, End: 14, Seconds: 62},
	}, got)
}

func TestParseTimeAnchorsAcceptsMultipleDigitMinutesAndHours(t *testing.T) {
	got := ParseTimeAnchors("123:45 100:02:03 0:00", 0)
	require.Equal(t, []int{7425, 360123, 0}, anchorSeconds(got))
}

func TestParseTimeAnchorsAllowsAdjacentProse(t *testing.T) {
	got := ParseTimeAnchors("看到1:02这里", 0)
	require.Equal(t, []int{62}, anchorSeconds(got))
}

func TestParseTimeAnchorsFiltersInvalidPartialAndOverDurationTokens(t *testing.T) {
	got := ParseTimeAnchors("1:60 1:99:02 1:02:60 12:34:56:07 5:00 4:59", 299)
	require.Equal(t, []int{299}, anchorSeconds(got))
}

func TestParseTimeAnchorsRejectsIntegerOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	content := fmt.Sprintf("%d:00 %d:00:00", maxInt/60+1, maxInt/3600+1)
	require.Empty(t, ParseTimeAnchors(content, 0))
}
