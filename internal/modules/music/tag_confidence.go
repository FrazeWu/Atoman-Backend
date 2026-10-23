package music

import (
	"fmt"
	"os"
	"strconv"
)

type tagConfidencePolicy struct{ MinVoters, DownPercent, Margin int }

func musicTagConfidencePolicy() tagConfidencePolicy {
	read := func(name string, fallback int) int {
		n, err := strconv.Atoi(os.Getenv(name))
		if err != nil || n < 1 {
			return fallback
		}
		return n
	}
	return tagConfidencePolicy{read("MUSIC_TAG_MIN_VOTERS", 10), read("MUSIC_TAG_DOWN_PERCENT", 80), read("MUSIC_TAG_DOWN_MARGIN", 6)}
}

func musicTagConfidence(up, down int64) string {
	p := musicTagConfidencePolicy()
	total := up + down
	if total >= int64(p.MinVoters) && down*100 >= total*int64(p.DownPercent) && down-up >= int64(p.Margin) {
		return "disputed"
	}
	if total >= int64(p.MinVoters) && up*100 >= total*int64(p.DownPercent) && up-down >= int64(p.Margin) {
		return "supported"
	}
	return "pending"
}

func musicTagConfidenceRank(value string) int {
	switch value {
	case "supported":
		return 2
	case "pending":
		return 1
	}
	return 0
}

// 与 DTO 使用同一个配置。只抑制关联，不删除标签或投票；先过滤再扩展祖先。
func musicTagSearchEligibleSQL() string {
	if os.Getenv("MUSIC_TAG_CONFIDENCE_FILTER") == "false" {
		return "TRUE"
	}
	p := musicTagConfidencePolicy()
	return fmt.Sprintf(`NOT EXISTS (SELECT 1 FROM music_tag_votes vote WHERE vote.assignment_id=assignment.id AND vote.deleted_at IS NULL
	 GROUP BY vote.assignment_id HAVING COUNT(*) >= %d AND COUNT(*) FILTER (WHERE vote.vote='down')*100 >= COUNT(*)*%d
	 AND COUNT(*) FILTER (WHERE vote.vote='down')-COUNT(*) FILTER (WHERE vote.vote='up') >= %d)`, p.MinVoters, p.DownPercent, p.Margin)
}
