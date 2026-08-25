package reputation

import (
	"math"
	"testing"
)

func TestQualityFromEvidenceStartsAtInitialQuality(t *testing.T) {
	mass, portfolio, stability, penalty := qualityFromEvidence(nil)
	if mass != 0 || portfolio != InitialQuality || penalty != 0 {
		t.Fatalf("empty evidence = mass=%v portfolio=%v penalty=%v", mass, portfolio, penalty)
	}
	if math.Abs(stability-0.8) > 0.0001 {
		t.Fatalf("empty stability = %v, want 0.8", stability)
	}
}

func TestQualityFromEvidenceUsesGeometricDecay(t *testing.T) {
	_, portfolio, _, _ := qualityFromEvidence([]blogEvidence{{quality: 80, share: 1}, {quality: 80, share: 1}})
	if math.Abs(portfolio-65) > 0.0001 {
		t.Fatalf("portfolio = %v, want 65", portfolio)
	}
}

func TestQualityFromEvidenceAllowsQualityToFallBelowInitial(t *testing.T) {
	_, portfolio, _, _ := qualityFromEvidence([]blogEvidence{{quality: 10, share: 1}})
	if math.Abs(portfolio-15) > 0.0001 {
		t.Fatalf("portfolio = %v, want 15", portfolio)
	}
}

func TestBlogQualityUsesPrior(t *testing.T) {
	weight := InitialQuality / 100
	quality := (BlogPriorStrength*BlogPriorMean + weight*100) / (BlogPriorStrength + weight)
	if math.Abs(quality-51.9230769) > 0.0001 {
		t.Fatalf("quality = %v", quality)
	}
}
