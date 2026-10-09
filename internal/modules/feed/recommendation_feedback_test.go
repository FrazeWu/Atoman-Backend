package feed

import "testing"

func TestRecommendationFeedbackValidation(t *testing.T) {
	if !validRecommendationFeedback("article", "hide") || !validRecommendationFeedback("channel", "less_source") {
		t.Fatal("expected supported recommendation feedback actions")
	}
	if validRecommendationFeedback("video", "hide") || validRecommendationFeedback("article", "rate") {
		t.Fatal("unsupported recommendation feedback must be rejected")
	}
}
