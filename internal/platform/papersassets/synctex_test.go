package papersassets

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestParseSyncTeXOnlyKeepsProjectFilesAndBoundedCoordinates(t *testing.T) {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	_, err := gz.Write([]byte("SyncTeX Version:1\nInput:1:/inputs/./main.tex\nInput:2:/etc/passwd\nInput:3:/inputs/chapter.tex\nInput:4:/inputs/../secret.tex\nMagnification:1000\nUnit:1\nX Offset:0\nY Offset:0\nContent:\n{1\nx1,2:6578176,6578176\nx2,1:6578176,6578176\nx1,99:6578176,6578176\nx3,1:6578176,13156352\nx4,1:6578176,6578176\nx1,2:9223372036854775807,6578176\n}1\nPostamble:\n"))
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	raw, err := ParseSyncTeX(&b, Snapshot{Files: []SourceFile{{Path: "main.tex", Content: "a\nb\nc"}, {Path: "chapter.tex", Content: "chapter"}}})
	require.NoError(t, err)
	var m SourceMap
	require.NoError(t, json.Unmarshal(raw, &m))
	require.Equal(t, []string{"main.tex", "chapter.tex"}, m.Files)
	require.Equal(t, [][5]int{{0, 2, 1, 1000, 1000}, {1, 1, 1, 1000, 2000}}, m.Points)
	require.NoError(t, ValidateSourceMap(raw))
	require.Error(t, ValidateSourceMap([]byte(`{"version":1,"files":["../private.tex"],"points":[[0,1,1,10,10]]}`)))
}
