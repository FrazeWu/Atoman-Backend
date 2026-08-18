package musicmedia

import "testing"

func TestAudioHeaderMatchesSupportedFormats(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		header      []byte
	}{
		{name: "mp3 id3", contentType: "audio/mpeg", header: []byte("ID3\x04\x00\x00")},
		{name: "mp3 frame", contentType: "audio/mpeg", header: []byte{0xff, 0xfb, 0x90, 0x64}},
		{name: "aac", contentType: "audio/aac", header: []byte{0xff, 0xf1, 0x50, 0x80}},
		{name: "flac", contentType: "audio/flac", header: []byte("fLaC\x00\x00")},
		{name: "wav", contentType: "audio/wav", header: []byte("RIFF\x00\x00\x00\x00WAVE")},
		{name: "m4a", contentType: "audio/x-m4a", header: []byte("\x00\x00\x00\x18ftypM4A ")},
		{name: "ogg", contentType: "audio/ogg", header: []byte("OggS\x00")},
		{name: "webm", contentType: "audio/webm", header: []byte{0x1a, 0x45, 0xdf, 0xa3}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !AudioHeaderMatches(test.header, test.contentType) {
				t.Fatalf("expected %s header to match", test.contentType)
			}
		})
	}
}

func TestAudioHeaderRejectsDeclaredTypeMismatch(t *testing.T) {
	if AudioHeaderMatches([]byte("not audio"), "audio/mpeg") {
		t.Fatal("plain text must not pass as MP3")
	}
	if AudioHeaderMatches([]byte("fLaC\x00"), "audio/mpeg") {
		t.Fatal("FLAC content must not pass as MP3")
	}
	if AudioHeaderMatches([]byte("ID3\x04\x00"), "application/octet-stream") {
		t.Fatal("unsupported declarations must be rejected")
	}
}
