package papersassets

import "encoding/json"

const (
	CompilerPDFLaTeX = "pdflatex"
	CompilerLuaLaTeX = "lualatex"
	CompilerXeLaTeX  = "xelatex"
)

// The empty value preserves old manifests and historical job defaults.
func NormalizeCompiler(value string) (string, bool) {
	switch value {
	case "", CompilerPDFLaTeX:
		return CompilerPDFLaTeX, true
	case CompilerLuaLaTeX:
		return CompilerLuaLaTeX, true
	case CompilerXeLaTeX:
		return CompilerXeLaTeX, true
	default:
		return "", false
	}
}

func (e Environment) SupportsCompiler(value string) bool {
	compiler, ok := NormalizeCompiler(value)
	if !ok || e.Validate() != nil {
		return false
	}
	switch compiler {
	case CompilerPDFLaTeX:
		return true
	case CompilerLuaLaTeX:
		return e == AcademicEnvironment() || e == AcademicCJKEnvironment()
	case CompilerXeLaTeX:
		return e == AcademicCJKEnvironment()
	default:
		return false
	}
}

func AcademicEnvironmentDigest() string {
	raw, _ := json.Marshal(AcademicEnvironment())
	return HashBytes(raw)
}

func AcademicCJKEnvironmentDigest() string {
	raw, _ := json.Marshal(AcademicCJKEnvironment())
	return HashBytes(raw)
}
