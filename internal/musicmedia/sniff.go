package musicmedia

import (
	"bytes"
	"io"
	"strings"
)

const sniffSize = 512

func MatchesDeclaredAudio(reader io.Reader, declared string) bool {
	var header [sniffSize]byte
	n, err := reader.Read(header[:])
	if err != nil && err != io.EOF {
		return false
	}
	return AudioHeaderMatches(header[:n], declared)
}

func AudioHeaderMatches(header []byte, declared string) bool {
	declared = strings.ToLower(strings.TrimSpace(strings.Split(declared, ";")[0]))
	switch declared {
	case "audio/mpeg":
		return hasMP3Header(header)
	case "audio/aac":
		return len(header) >= 2 && header[0] == 0xff && (header[1]&0xf6) == 0xf0
	case "audio/flac":
		return bytes.HasPrefix(header, []byte("fLaC"))
	case "audio/wav", "audio/x-wav", "audio/vnd.wave":
		return len(header) >= 12 && bytes.Equal(header[:4], []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WAVE"))
	case "audio/mp4", "audio/x-m4a":
		return len(header) >= 12 && bytes.Equal(header[4:8], []byte("ftyp"))
	case "audio/ogg", "application/ogg":
		return bytes.HasPrefix(header, []byte("OggS"))
	case "audio/webm":
		return len(header) >= 4 && bytes.Equal(header[:4], []byte{0x1a, 0x45, 0xdf, 0xa3})
	default:
		return false
	}
}

func hasMP3Header(header []byte) bool {
	if bytes.HasPrefix(header, []byte("ID3")) {
		return true
	}
	for index := 0; index+1 < len(header) && index < 32; index++ {
		if header[index] == 0xff && header[index+1]&0xe0 == 0xe0 && header[index+1]&0x18 != 0x08 {
			return true
		}
	}
	return false
}
