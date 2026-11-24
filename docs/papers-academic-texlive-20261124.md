# Academic TeX Live release candidate (2026-11-24)

Derived from the already verified P0 base. The official TeX Live 2026 repository was signature-verified during installation. No production project or credential was included in the image build. The independently isolated original research-paper acceptance produced a 29-page, 1,036,366-byte PDF with actual resource enforcement and verified cleanup. Only one pdfLaTeX pass is currently run; bibliography and cross-references may remain unresolved.

The exact derived image configuration ID and normalized archive are pinned separately in AcademicEnvironment. This is a candidate, not an activated release. Publish via the existing operator-only signed release service and verify the signed remote archive before changing both API and launcher environment pins. Keep the legacy environment and its trust configuration for rollback. No security gate is modified by this commit.

```json
{
  "environment": {
    "version": 2,
    "name": "texlive-2026-academic-20261124",
    "image_digest": "sha256:2738e65c08d33592eb5bdc14cd46f3662c89f6f611a466b2e92f27c8b97c6173",
    "archive_digest": "sha256:83f5c23b237e83a05e73ab160b4ab6e3dd0a8597ccfb941c681e7fb77207ebe7",
    "runsc_release": "20261019.0"
  },
  "environment_digest": "sha256:6c99162c4ba767041ae47209298a7070cd1b801f8e79230fbfc2e8519c8ee036",
  "base_image_digest": "sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4",
  "archive_bytes": 1284185088,
  "packages_manifest_sha256": "2183f9a3a4ddf5eda17b9b7e151254f197804a154c634231c132cbcf6a3ce5b0",
  "acceptance": {
    "pdf_pages": 29,
    "pdf_bytes": 1036366,
    "gvisor_limits_verified": true,
    "cleanup_verified": true,
    "single_pass": true
  }
}
```
