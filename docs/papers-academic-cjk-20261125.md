# Academic CJK environment candidate

This local release candidate derives from the verified academic TeX Live 2026
base (Docker configuration ID 2738e65c08d33592eb5bdc14cd46f3662c89f6f611a466b2e92f27c8b97c6173).
The build uses TeX Live's required repository signature verification and the
base Debian distribution's authenticated package repository. The candidate
adds XeTeX, CTeX, xeCJK, their dependencies and Noto CJK fonts. Fandol was not
available in the verified TeX Live repository; no unverified font downloads
are permitted. Existing manuscripts that specify unavailable proprietary
fonts or Fandol may require their font configuration to be changed.

Example for XeLaTeX:
```tex
\documentclass[UTF8,fontset=none]{ctexart}
\setCJKmainfont{Noto Serif CJK SC}
\setCJKsansfont{Noto Sans CJK SC}
\begin{document}
\section{研究背景}
中文论文与 English 混合排版。
\end{document}
```

XeLaTeX uses a fixed, byte-verified driver with no shell escape. The
immutable snapshot and authoritative job must agree on the engine. The
new engine is visible only for the exact CJK environment pin, with
enable_xelatex=true in both protected API and launcher configuration.
The default remains pdfLaTeX. Resource limits, archive normalization,
network denial, rootfs read-only policy, and existing PDF publication
boundaries are unchanged.

Build from the task worktree using scripts/papers/academic-cjk/build-candidate.sh.
A new build is a new candidate, not an automatically approved environment.
Pin the actual normalized archive digest and image identity in reviewed code.
Run the opt-in synthetic gVisor test with PAPERS_ACADEMIC_CJK_TEST_ARCHIVE
pointing to the local normalized archive. Before production release, use the
existing operator-only signed release workflow; verify the remote archive,
both trust pins and compiler capability after deployment. Do not enable the
new engine on the old archive or bypass production signing.

The separate original academic archive must be retained for rollback.

## Candidate provenance

- Image ID: sha256:29321d21d7d552a72ac460004360d4a7c267c6d99f6b323d3ce951694755cf58
- Normalized archive: sha256:296eb07895af549c3258ade69e8c2e1e1f55181606f832680ccb7bdc1fa9beac
- Archive size: 1602460160 bytes; 44808 entries, 86 normalized absolute symlinks.
- TeX package manifest SHA-256: 1a2e0cac2899e7756223294810fb39c7b2ac47d77b39c73a1d87d102e7a16d1c
- Noto CJK package: 1:20240730+repack1-1
- Font package manifest SHA-256: c05ee1f4d91e858013491218cd7adc56e2e6cb02b87e74eed5c011d6e5a44024
- XeTeX package revision 80015; CTeX revision 81260; xeCJK revision 80059.

## Local acceptance (2026-11-25)

The normalized archive passed the strict trusted TAR scanner. The synthetic
Chinese XeLaTeX test passed under real gVisor: a 26247-byte PDF, three XeLaTeX
passes and BibTeX, verified nonempty SyncTeX source map, no missing-character
warning, shell escape disabled, enforced limits and verified cleanup. A
separate offline Docker smoke test produced a 15255-byte PDF, with a font
shape substitution warning. This is not a production signed release and
is not evidence of full compatibility with arbitrary thesis templates.
