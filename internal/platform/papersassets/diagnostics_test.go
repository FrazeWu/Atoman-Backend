package papersassets

import (
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func diagnosticSnapshot() Snapshot {
	return Snapshot{Version: 1, ProjectID: uuid.New(), Revision: 1, Files: []SourceFile{
		{Path: "main.tex", Content: "one\ntwo\nthree\n"},
		{Path: "chapters/results.tex", Content: "one\ntwo\nthree\n"},
		{Path: "refs.bib", Content: "one\ntwo\nthree\n"},
	}}
}
func TestCompileDiagnosticsExactProjectLocations(t *testing.T) {
	s := diagnosticSnapshot()
	d := ParseCompileDiagnostics("./chapters/results.tex:2: Undefined control sequence.\nl.2 \\badcommand\n./main.tex:3: LaTeX Error: File 'missing.sty' not found.\n",
		"I was expecting a ','---line 2 of file /inputs/refs.bib", s)
	require.Len(t, d, 3)
	require.Equal(t, CompileDiagnostic{Tool: "latex", File: "chapters/results.tex", Line: 2, Message: "Undefined control sequence."}, d[0])
	require.Equal(t, "main.tex", d[1].File)
	require.Contains(t, d[1].Message, "missing.sty")
	require.Equal(t, "refs.bib", d[2].File)
	require.Equal(t, 2, d[2].Line)
	raw := EncodeCompileDiagnostics(d)
	require.NotEmpty(t, raw)
	require.Equal(t, d, DecodeCompileDiagnostics(raw))
}
func TestDiagnosticsRejectForeignPathsAndBoundMetadata(t *testing.T) {
	d := ParseCompileDiagnostics("/root/private/main.tex:2: Undefined control sequence.\n./main.tex:999: LaTeX Error: File '/host/private/secret' not found.\n! LaTeX Error: "+strings.Repeat("x", 400)+"\n", "", diagnosticSnapshot())
	require.Len(t, d, 3)
	for _, x := range d {
		require.Empty(t, x.File)
		require.Zero(t, x.Line)
		require.LessOrEqual(t, len(x.Message), 360)
	}
	raw := EncodeCompileDiagnostics(d)
	require.NotEmpty(t, raw)
	require.NotContains(t, raw, "/root")
	require.NotContains(t, raw, "/host")
	require.Empty(t, EncodeCompileDiagnostics([]CompileDiagnostic{{Tool: "latex", File: "../secret.tex", Line: 1, Message: "Error"}}))
	require.Empty(t, EncodeCompileDiagnostics([]CompileDiagnostic{{Tool: "latex", Message: "secret /host/path"}}))
	require.Empty(t, DecodeCompileDiagnostics(strings.Repeat("a", MaxDiagnosticsBytes+1)))
}
func TestCompileDiagnosticsBoundAndDeduplicate(t *testing.T) {
	raw := strings.Repeat("./main.tex:1: Undefined control sequence.\n", 30)
	require.Len(t, ParseCompileDiagnostics(raw, "", diagnosticSnapshot()), 1)
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, "! Error: "+strings.Repeat("x", i+1))
	}
	require.Len(t, ParseCompileDiagnostics(strings.Join(lines, "\n"), "", diagnosticSnapshot()), MaxDiagnostics)
}
