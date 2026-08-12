package music

import (
	"context"
	"encoding/binary"
	"errors"
	"sort"
)

const WaveformPeakCount = 280

// GenerateWaveformPeaks downsamples an audio file and returns compact 0-100 peaks.
func GenerateWaveformPeaks(ctx context.Context, runner MediaCommandRunner, audioPath string) ([]int, error) {
	if runner == nil {
		return nil, errors.New("media command runner is required")
	}
	raw, err := runner.Run(ctx, "ffmpeg", "-v", "error", "-i", audioPath, "-vn", "-ac", "1", "-ar", "8000", "-f", "s16le", "pipe:1")
	if err != nil {
		return nil, err
	}
	peaks := waveformPeaksFromPCM(raw, WaveformPeakCount)
	if len(peaks) != WaveformPeakCount {
		return nil, errors.New("audio produced no waveform samples")
	}
	return peaks, nil
}

func waveformPeaksFromPCM(raw []byte, peakCount int) []int {
	if peakCount <= 0 || len(raw) < 2 {
		return nil
	}
	sampleCount := len(raw) / 2
	peaks := make([]int, peakCount)
	for index := range peaks {
		start := index * sampleCount / peakCount
		end := (index + 1) * sampleCount / peakCount
		if end <= start {
			end = start + 1
		}
		for sampleIndex := start; sampleIndex < end && sampleIndex < sampleCount; sampleIndex++ {
			sample := int(int16(binary.LittleEndian.Uint16(raw[sampleIndex*2:])))
			if sample < 0 {
				sample = -sample
			}
			if sample > peaks[index] {
				peaks[index] = sample
			}
		}
	}
	return normalizeWaveformPeaks(peaks)
}

func normalizeWaveformPeaks(peaks []int) []int {
	if len(peaks) == 0 {
		return peaks
	}
	sorted := append([]int(nil), peaks...)
	sort.Ints(sorted)
	reference := sorted[(len(sorted)-1)*95/100]
	if reference < 1 {
		reference = 1
	}
	for index, peak := range peaks {
		value := peak * 100 / reference
		if value < 8 {
			value = 8
		}
		if value > 100 {
			value = 100
		}
		peaks[index] = value
	}
	return peaks
}
