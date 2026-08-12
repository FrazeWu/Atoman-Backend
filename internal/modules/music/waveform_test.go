package music

import (
	"encoding/binary"
	"testing"
)

func TestWaveformPeaksFromPCM(t *testing.T) {
	samples := []int16{0, 1000, -2000, 4000, -8000, 16000, -24000, 32767}
	raw := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(raw[index*2:], uint16(sample))
	}

	peaks := waveformPeaksFromPCM(raw, 4)
	if len(peaks) != 4 {
		t.Fatalf("len(peaks) = %d, want 4", len(peaks))
	}
	if peaks[0] > peaks[1] || peaks[1] > peaks[2] || peaks[2] > peaks[3] {
		t.Fatalf("peaks are decreasing: %#v", peaks)
	}
	if peaks[3] != 100 {
		t.Fatalf("last peak = %d, want 100", peaks[3])
	}
}

func TestWaveformPeaksKeepSilenceVisible(t *testing.T) {
	peaks := waveformPeaksFromPCM(make([]byte, 64), 4)
	for _, peak := range peaks {
		if peak != 8 {
			t.Fatalf("silent peaks = %#v, want all 8", peaks)
		}
	}
}
