//go:build ignore

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureBytes(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func fixtureDocument(t *testing.T, value any) evidenceDocument {
	t.Helper()
	d, err := decodeEvidence(fixtureBytes(t, value))
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func fixtureHash(letter string) string { return strings.Repeat(letter, 64) }

func recoveryFixture(t *testing.T, active bool) (evidenceDocument, recoveryExpectations, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	completed := now.Add(-time.Hour).Format(time.RFC3339Nano)
	observed := now.Add(-2 * time.Minute).Format(time.RFC3339Nano)
	snapshot := "20261105T130000Z-abcdef12"
	capsuleID := snapshot + "m"
	fullSHA, capsuleSHA := fixtureHash("a"), fixtureHash("b")
	counts := map[string]int64{"Users": 42, "mail_accounts": 1, "mail_invites": 0, "mail_access_credentials": 0}
	absent := []string{}
	for _, table := range applicationMailTables {
		if _, ok := counts[table]; !ok {
			if active {
				counts[table] = 0
			} else {
				absent = append(absent, table)
			}
		}
	}
	allCounts := map[string]int64{"unrelated_site_table": 99999}
	for key, value := range counts {
		allCounts[key] = value
	}
	scope, phase := "pre_activation_mail_capsule", "pre_activation"
	if active {
		scope, phase = "post_activation_mail_capsule", "active"
	}
	source := map[string]any{"format": "atoman-postgres-logical-v1", "complete": true, "mail_assets_verified": true, "completed_at": completed, "verification_profile": "full", "backups": []any{
		map[string]any{"database": "atoman_prod", "dump": "atoman_prod.dump", "sha256": fixtureHash("c"), "restore_verified": true, "restore_profile": "full", "public_table_counts": allCounts},
		map[string]any{"database": "atoman_dev", "dump": "atoman_dev.dump", "sha256": fixtureHash("d"), "restore_verified": true, "restore_profile": "full", "public_table_counts": map[string]int64{"Users": 3}},
		map[string]any{"database": "atoman_mail", "dump": "atoman_mail.dump", "sha256": fixtureHash("e"), "restore_verified": true, "restore_profile": "full", "public_table_counts": map[string]int64{"data_store": 9}},
	}, "additional_files": []any{map[string]any{"name": "mail-assets.tar", "sha256": fixtureHash("f"), "verified": true}}}
	expected := recoveryExpectations{Snapshot: snapshot, FullCiphertextSHA256: fullSHA, SourceManifest: fixtureBytes(t, source)}
	sourceReceipt := map[string]any{"format": "atoman-encrypted-export-v1", "ciphertext": "backup.tar.age", "source_snapshot_id": snapshot, "source_completed_at": completed, "sha256": fullSHA, "bytes": int64(825000000), "offsite_verified": false}
	expected.SourceExportReceipt = fixtureBytes(t, sourceReceipt)
	objectCounts := map[string]int64{"atoman-mail-prod": 2, "atoman-mail-pending": 0}
	buckets := []string{"atoman-mail-pending", "atoman-mail-prod"}
	capsuleManifest := fixtureDocument(t, map[string]any{
		"format": "atoman-mail-recovery-capsule-v1", "scope": scope, "capsule_id": capsuleID, "source_snapshot_id": snapshot, "source_completed_at": completed,
		"source_manifest_sha256": evidenceDigest(expected.SourceManifest), "source_full_ciphertext_sha256": fullSHA, "source_full_ciphertext_bytes": int64(825000000),
		"atoman_prod_dump_sha256": fixtureHash("c"), "atoman_mail_dump_sha256": fixtureHash("e"), "mail_assets_sha256": fixtureHash("f"),
		"r2_inventory_sha256": fixtureHash("1"), "runtime_files_sha256": fixtureHash("2"), "application_payload_sha256": fixtureHash("3"), "application_manifest_sha256": fixtureHash("4"),
		"r2_buckets": buckets, "r2_object_counts": objectCounts, "runtime_file_count": 8, "application_source_tables": counts, "application_absent_mail_tables": absent,
	})
	canonical, err := canonicalCapsuleManifest(capsuleManifest)
	if err != nil {
		t.Fatal(err)
	}
	capsuleManifestSHA := evidenceDigest(canonical)
	capsuleReceipt := map[string]any{"format": "atoman-encrypted-export-v1", "ciphertext": "backup.tar.age", "source_snapshot_id": capsuleID, "paired_source_snapshot_id": snapshot, "source_completed_at": completed, "sha256": capsuleSHA, "bytes": int64(1200000), "backup_scope": "mail-complete", "capsule_manifest_sha256": capsuleManifestSHA, "offsite_verified": false}
	expected.CapsuleReceipt = fixtureBytes(t, capsuleReceipt)
	marker := map[string]any{"format": "atoman-nas-copy-v1", "source": capsuleID, "source_snapshot_id": capsuleID, "source_completed_at": completed, "sha256": capsuleSHA, "bytes": int64(1200000), "ciphertext_transferred": true, "downloaded_at": observed, "recovery_verified": false}
	report := map[string]any{
		"format": recoveryV2, "scope": scope, "status": "ok", "restore_succeeded": true, "source_snapshot_id": snapshot, "source_completed_at": completed, "verified_at": now.Format(time.RFC3339Nano),
		"source_manifest_sha256": evidenceDigest(expected.SourceManifest), "source_full_ciphertext_sha256": fullSHA, "source_full_ciphertext_bytes": int64(825000000),
		"capsule_id": capsuleID, "capsule_sha256": capsuleSHA, "capsule_bytes": int64(1200000), "capsule_manifest": capsuleManifest, "capsule_manifest_sha256": capsuleManifestSHA,
		"main_bundle_nas_verified": false, "full_snapshot_nas_verified": false, "capsule_nas_roundtrip_verified": true, "all_mail_payload_files_verified": true, "all_snapshot_files_sha256_verified": false,
		"owner_offline_identity_verified": true, "isolated_network_internal": true, "s3_credentials_disposable": true, "postgres_row_counts_match": true, "canary_uid_preserved": true,
		"message_bytes_match": true, "body_sha256_matches": true, "attachment_bytes_match": true, "production_data_modified": false, "mailbox": "INBOX", "verified_file_count": 6,
		"message_sha256": fixtureHash("5"), "message_expected_sha256": fixtureHash("5"), "body_sha256": fixtureHash("6"), "body_expected_sha256": fixtureHash("6"), "attachment_sha256": fixtureHash("7"), "attachment_expected_sha256": fixtureHash("7"),
		"main_bundle_nas": map[string]any{"status": "pending", "source_snapshot_id": snapshot, "sha256": fullSHA, "bytes": int64(825000000), "observed_at": observed, "partial_bytes": int64(6300000), "mac_full_ciphertext_readback": false},
		"capsule_nas":     map[string]any{"capsule_id": capsuleID, "paired_source_snapshot_id": snapshot, "nas_path": nasSnapshotRoot + capsuleID + "/backup.tar.age", "sha256": capsuleSHA, "bytes": int64(1200000), "readback_sha256": capsuleSHA, "readback_bytes": int64(1200000), "verified_at": observed, "receipt": capsuleReceipt, "copy_marker": marker},
		"mail_assets":     map[string]any{"objects": 2, "runtime_files": 8},
		"mail_storage":    map[string]any{"atoman_mail_dump_sha256": fixtureHash("e"), "mail_assets_sha256": fixtureHash("f"), "r2_inventory_sha256": fixtureHash("1"), "r2_buckets": buckets, "r2_object_counts": objectCounts, "runtime_files_sha256": fixtureHash("2"), "runtime_file_count": 8, "all_r2_objects_restored": true, "all_runtime_files_verified": true},
		"mail_metadata":   map[string]any{"format": "atoman-mail-application-recovery-v1", "phase": phase, "source_database": "atoman_prod", "source_dump_sha256": fixtureHash("c"), "source_tables": counts, "restored_tables": counts, "absent_mail_tables": absent, "table_counts_match": true, "user_links_verified": true, "credential_account_links_verified": true, "draft_owner_links_verified": true, "restore_succeeded": true, "application_payload_sha256": fixtureHash("3"), "application_manifest_sha256": fixtureHash("4")},
	}
	return fixtureDocument(t, report), expected, now
}

func TestActivationV2CompleteMailAcceptsHonestPendingSiteBundle(t *testing.T) {
	for _, active := range []bool{false, true} {
		r, expected, now := recoveryFixture(t, active)
		if _, err := validateRecoveryEvidence(fixtureBytes(t, r), expected, now); err != nil {
			t.Fatalf("active=%v: %v", active, err)
		}
	}
}
func TestActivationV2FullBundleClaimNeedsItsOwnEvidence(t *testing.T) {
	r, expected, now := recoveryFixture(t, false)
	r["main_bundle_nas_verified"], r["full_snapshot_nas_verified"] = true, true
	if _, err := validateRecoveryEvidence(fixtureBytes(t, r), expected, now); err == nil {
		t.Fatal("false full NAS claim accepted")
	}
	receipt, err := decodeEvidence(expected.SourceExportReceipt)
	if err != nil {
		t.Fatal(err)
	}
	r["main_bundle_nas"] = fixtureDocument(t, map[string]any{"source_snapshot_id": expected.Snapshot, "sha256": expected.FullCiphertextSHA256, "bytes": 825000000, "nas_path": nasSnapshotRoot + expected.Snapshot + "/backup.tar.age", "verification_method": "nas_pull_full_ciphertext_sha256", "mac_full_ciphertext_readback": false, "verified_at": now.Format(time.RFC3339Nano), "receipt": receipt, "pull_status": map[string]any{"state": "ok", "source_snapshot_id": expected.Snapshot}, "copy_marker": map[string]any{"format": "atoman-nas-copy-v1", "source": expected.Snapshot, "source_snapshot_id": expected.Snapshot, "sha256": expected.FullCiphertextSHA256, "bytes": 825000000, "ciphertext_transferred": true, "downloaded_at": now.Format(time.RFC3339Nano)}})
	if _, err := validateRecoveryEvidence(fixtureBytes(t, r), expected, now); err != nil {
		t.Fatal(err)
	}
	evidenceObject(evidenceObject(r["main_bundle_nas"])["copy_marker"])["sha256"] = fixtureHash("9")
	if _, err := validateRecoveryEvidence(fixtureBytes(t, r), expected, now); err == nil {
		t.Fatal("wrong full NAS marker accepted")
	}
}
func TestActivationV2RejectsMixedMissingAndExpiredEvidence(t *testing.T) {
	cases := map[string]func(evidenceDocument, *recoveryExpectations, time.Time){
		"wrong snapshot": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			r["source_snapshot_id"] = "20261105T130000Z-wrong123"
		},
		"wrong full bundle": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			r["source_full_ciphertext_sha256"] = fixtureHash("9")
		},
		"wrong capsule": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) { r["capsule_sha256"] = fixtureHash("9") },
		"capsule impersonates full": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			r["capsule_sha256"] = r["source_full_ciphertext_sha256"]
		},
		"wrong source manifest": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			r["source_manifest_sha256"] = fixtureHash("9")
		},
		"source file changed": func(_ evidenceDocument, e *recoveryExpectations, _ time.Time) {
			e.SourceManifest = append(e.SourceManifest, '\n')
		},
		"missing source receipt":  func(_ evidenceDocument, e *recoveryExpectations, _ time.Time) { e.SourceExportReceipt = nil },
		"missing capsule receipt": func(_ evidenceDocument, e *recoveryExpectations, _ time.Time) { e.CapsuleReceipt = nil },
		"wrong capsule manifest digest": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			r["capsule_manifest_sha256"] = fixtureHash("9")
		},
		"capsule manifest edited": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["capsule_manifest"])["runtime_file_count"] = json.Number("7")
		},
		"old full snapshot claim": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			r["all_snapshot_files_sha256_verified"] = true
		},
		"ambiguous ciphertext": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			r["ciphertext_sha256"] = r["capsule_sha256"]
		},
		"ambiguous NAS":       func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) { r["nas_roundtrip_verified"] = true },
		"missing capsule NAS": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) { delete(r, "capsule_nas") },
		"capsule NAS unverified": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			r["capsule_nas_roundtrip_verified"] = false
		},
		"capsule NAS readback wrong": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["capsule_nas"])["readback_sha256"] = fixtureHash("9")
		},
		"capsule NAS bytes wrong": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["capsule_nas"])["readback_bytes"] = json.Number("1")
		},
		"capsule NAS points to full": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["capsule_nas"])["nas_path"] = nasSnapshotRoot + evidenceText(r, "source_snapshot_id") + "/backup.tar.age"
		},
		"capsule NAS receipt wrong": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(evidenceObject(r["capsule_nas"])["receipt"])["sha256"] = fixtureHash("9")
		},
		"capsule NAS marker wrong": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(evidenceObject(r["capsule_nas"])["copy_marker"])["source"] = r["source_snapshot_id"]
		},
		"main NAS identity wrong": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["main_bundle_nas"])["sha256"] = fixtureHash("9")
		},
		"main falsely read back on Mac": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["main_bundle_nas"])["mac_full_ciphertext_readback"] = true
		},
		"pending main overclaims bytes": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["main_bundle_nas"])["partial_bytes"] = json.Number("825000001")
		},
		"missing mail metadata": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) { delete(r, "mail_metadata") },
		"missing Users": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			delete(evidenceObject(evidenceObject(r["mail_metadata"])["source_tables"]), "Users")
		},
		"missing existing mail table": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			delete(evidenceObject(evidenceObject(r["mail_metadata"])["restored_tables"]), "mail_accounts")
		},
		"wrong owner links": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["mail_metadata"])["user_links_verified"] = false
		},
		"wrong application source": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["mail_metadata"])["source_dump_sha256"] = fixtureHash("9")
		},
		"wrong application artifact": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["mail_metadata"])["application_payload_sha256"] = fixtureHash("9")
		},
		"preactivation relabelled active": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			r["scope"] = "post_activation_mail_capsule"
		},
		"wrong native mail dump": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["mail_storage"])["atoman_mail_dump_sha256"] = fixtureHash("9")
		},
		"wrong assets": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["mail_storage"])["mail_assets_sha256"] = fixtureHash("9")
		},
		"wrong inventory": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["mail_storage"])["r2_inventory_sha256"] = fixtureHash("9")
		},
		"missing R2 bucket": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["mail_storage"])["r2_buckets"] = []any{"atoman-mail-prod"}
		},
		"runtime not restored": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) {
			evidenceObject(r["mail_storage"])["all_runtime_files_verified"] = false
		},
		"wrong content hash": func(r evidenceDocument, _ *recoveryExpectations, _ time.Time) { r["body_sha256"] = fixtureHash("9") },
		"expired recovery": func(r evidenceDocument, _ *recoveryExpectations, now time.Time) {
			r["verified_at"] = now.Add(-49 * time.Hour).Format(time.RFC3339Nano)
		},
		"future recovery": func(r evidenceDocument, _ *recoveryExpectations, now time.Time) {
			r["verified_at"] = now.Add(6 * time.Minute).Format(time.RFC3339Nano)
		},
		"expired capsule NAS": func(r evidenceDocument, _ *recoveryExpectations, now time.Time) {
			evidenceObject(r["capsule_nas"])["verified_at"] = now.Add(-49 * time.Hour).Format(time.RFC3339Nano)
		},
		"expired main observation": func(r evidenceDocument, _ *recoveryExpectations, now time.Time) {
			evidenceObject(r["main_bundle_nas"])["observed_at"] = now.Add(-49 * time.Hour).Format(time.RFC3339Nano)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r, e, now := recoveryFixture(t, false)
			mutate(r, &e, now)
			if _, err := validateRecoveryEvidence(fixtureBytes(t, r), e, now); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}
func TestActivationV1RemainsCompatibleWithoutCapsuleRelabelling(t *testing.T) {
	r, expected, now := recoveryFixture(t, false)
	common := evidenceDocument{}
	for _, key := range []string{"status", "restore_succeeded", "source_snapshot_id", "verified_at", "owner_offline_identity_verified", "isolated_network_internal", "s3_credentials_disposable", "postgres_row_counts_match", "canary_uid_preserved", "message_bytes_match", "body_sha256_matches", "attachment_bytes_match", "production_data_modified", "message_sha256", "message_expected_sha256", "body_sha256", "body_expected_sha256", "attachment_sha256", "attachment_expected_sha256"} {
		common[key] = r[key]
	}
	common["format"], common["ciphertext_sha256"] = recoveryV1, expected.FullCiphertextSHA256
	common["nas_roundtrip_verified"], common["all_snapshot_files_sha256_verified"] = true, true
	expected.SourceManifest, expected.SourceExportReceipt, expected.CapsuleReceipt = nil, nil, nil
	if _, err := validateRecoveryEvidence(fixtureBytes(t, common), expected, now); err != nil {
		t.Fatal(err)
	}
	common["capsule_id"] = r["capsule_id"]
	if _, err := validateRecoveryEvidence(fixtureBytes(t, common), expected, now); err == nil {
		t.Fatal("capsule relabelled as full v1 accepted")
	}
}
func TestCapsuleCanonicalJSONMatchesPythonASCIIEncoding(t *testing.T) {
	d := fixtureDocument(t, map[string]any{"z": "<>&中文😀\u2028\x7f\n", "a": []any{1, true, nil}})
	got, err := canonicalCapsuleManifest(d)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":[1,true,null],"z":"<>&\u4e2d\u6587\ud83d\ude00\u2028\u007f\n"}`
	if string(got) != want {
		t.Fatalf("canonical bytes mismatch: got %q want %q", got, want)
	}
	d["float"] = json.Number("1.0")
	if _, err := canonicalCapsuleManifest(d); err == nil {
		t.Fatal("uncontracted float accepted")
	}
}
func TestAdminPreflightDisablesProxyRedirectAndOtherEndpoints(t *testing.T) {
	var redirected atomic.Int32
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(http.StatusOK) }))
	defer next.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, next.URL, http.StatusFound) }))
	defer first.Close()
	t.Setenv("HTTP_PROXY", next.URL)
	t.Setenv("HTTPS_PROXY", next.URL)
	client := newStalwartPreflightClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("preflight may use an environment proxy")
	}
	if _, err := client.Get(first.URL); err == nil {
		t.Fatal("preflight followed redirect")
	}
	if redirected.Load() != 0 {
		t.Fatal("redirect received credentials or network traffic")
	}
	if err := preflightStalwartAdmin(next.URL, "synthetic", "not-a-real-secret"); err == nil {
		t.Fatal("nonfixed endpoint accepted")
	}
	if redirected.Load() != 0 {
		t.Fatal("nonfixed endpoint received a credential request")
	}
}
