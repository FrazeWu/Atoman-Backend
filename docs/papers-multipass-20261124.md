# Academic multipass compilation
The signed academic environment uses a fixed driver embedded in the trusted
Launcher. The driver is staged outside the project source directory and
verified byte for byte before guest execution. It does not load project
latexmk configurations or execute user-controlled shell commands.
It runs pdfLaTeX twice, or three times after a required BibTeX pass, with
an additional pass when cross-reference warnings require it (maximum
four pdfLaTeX invocations). BibTeX runs against the fixed main.aux in
the bounded output directory. The sandbox remains read-only outside its
existing writable output and scratch mounts, with no network, no TeX
shell escape, and unchanged resource limits and timeout.
Legacy P0 environment compilation is unchanged. The signed academic
archive and trust pin are unchanged. This change is Launcher-only;
no database migration is required.
Acceptance: policy, worker, launcher, runtime and asset tests passed.
Synthetic reference fixture: 3 pdfLaTeX passes, 1 BibTeX pass, zero
final unresolved citation/reference warnings. Existing research paper:
4 pdfLaTeX passes, 1 BibTeX pass, zero final unresolved citation/reference
warnings, 1,059,683-byte PDF, actual isolation and cleanup verified.
