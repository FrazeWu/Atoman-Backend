package papersassets

// AcademicCJKEnvironment pins the locally built and normalized CJK candidate.
// Production still requires the separate signed release and operator opt-in.
func AcademicCJKEnvironment() Environment {
	return Environment{
		Version:       3,
		Name:          "texlive-2026-academic-cjk-20261125",
		ImageDigest:   "sha256:29321d21d7d552a72ac460004360d4a7c267c6d99f6b323d3ce951694755cf58",
		ArchiveDigest: "sha256:296eb07895af549c3258ade69e8c2e1e1f55181606f832680ccb7bdc1fa9beac",
		RunscRelease:  "20261019.0",
	}
}
