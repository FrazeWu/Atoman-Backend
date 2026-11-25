package papersassets

import "encoding/json"

const (
	CompilerPDFLaTeX = "pdflatex"
	CompilerLuaLaTeX = "lualatex"
)

// The empty value preserves old manifests and historical job defaults.
func NormalizeCompiler(value string) (string, bool) {
	switch value {
	case "", CompilerPDFLaTeX:
		return CompilerPDFLaTeX, true
	case CompilerLuaLaTeX:
		return CompilerLuaLaTeX, true
	default:
		return "", false
	}
}

func (e Environment) SupportsCompiler(value string) bool {
	compiler, ok := NormalizeCompiler(value)
	if !ok || e.Validate() != nil {
		return false
	}
	return compiler == CompilerPDFLaTeX || e == AcademicEnvironment()
}

func AcademicEnvironmentDigest() string {
	raw, _ := json.Marshal(AcademicEnvironment())
	return HashBytes(raw)
}
