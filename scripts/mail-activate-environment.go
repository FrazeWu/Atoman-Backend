//go:build ignore

// Operator-only activation. A dated NAS recovery receipt is required before
// any mail gate is written. Credentials never appear in command output.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/joho/godotenv"
)

func privateFile(path string) ([]byte, error) {
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private regular file required")
	}
	return os.ReadFile(path)
}

const recoveryV1 = "atoman-mail-nas-recovery-v1"
const recoveryV2 = "atoman-mail-nas-recovery-v2"
const stalwartPreflightEndpoint = "http://127.0.0.1:18580"
const nasSnapshotRoot = "/mnt/basic_pool/AtomanBackups/snapshots/"
const evidenceMaxAge = 48 * time.Hour

var evidenceHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var evidenceSnapshotPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[a-z0-9]{6,20}$`)
var applicationMailTables = []string{"mail_accounts", "mail_invites", "mail_access_credentials", "mail_pending_sends", "mail_drafts", "mail_uploads", "mail_preferences", "mail_action_receipts", "mail_retention"}

type evidenceDocument map[string]any

type recoveryExpectations struct {
	Snapshot             string
	FullCiphertextSHA256 string
	SourceManifest       []byte
	SourceExportReceipt  []byte
	CapsuleReceipt       []byte
}

type activationOptions struct {
	recoveryReport, snapshot, ciphertextSHA, mailEnv, adminJSON string
	sourceManifest, sourceExportReceipt, capsuleReceipt         string
	apply                                                       bool
}

// Metadata receipts contain no credentials. They may retain the read-only
// permissions needed by the existing backup publisher; do not change them.
func evidenceFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 16<<20 {
		return nil, errors.New("read-only regular evidence file required")
	}
	return os.ReadFile(path)
}
func decodeEvidence(raw []byte) (evidenceDocument, error) {
	if len(raw) == 0 || len(raw) > 16<<20 || !utf8.Valid(raw) {
		return nil, errors.New("invalid evidence document")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result evidenceDocument
	if decoder.Decode(&result) != nil || result == nil {
		return nil, errors.New("invalid evidence document")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("trailing evidence data")
	}
	return result, nil
}
func evidenceObject(value any) evidenceDocument {
	if value, ok := value.(map[string]any); ok {
		return evidenceDocument(value)
	}
	if value, ok := value.(evidenceDocument); ok {
		return value
	}
	return nil
}
func evidenceText(d evidenceDocument, key string) string { value, _ := d[key].(string); return value }
func evidenceInteger(value any) (int64, bool) {
	switch n := value.(type) {
	case json.Number:
		v, e := strconv.ParseInt(string(n), 10, 64)
		return v, e == nil
	case int64:
		return n, true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}
func evidenceCount(d evidenceDocument, key string) (int64, bool) {
	v, ok := evidenceInteger(d[key])
	return v, ok && v >= 0
}
func evidenceHash(d evidenceDocument, key string) string {
	value := evidenceText(d, key)
	if !evidenceHashPattern.MatchString(value) {
		return ""
	}
	return value
}
func evidenceDigest(raw []byte) string {
	value := sha256.Sum256(raw)
	return hex.EncodeToString(value[:])
}
func evidenceFailure(field string) error { return errors.New("recovery evidence mismatch: " + field) }
func requireEvidence(d evidenceDocument, keys ...string) error {
	for _, key := range keys {
		if d[key] != true {
			return evidenceFailure(key)
		}
	}
	return nil
}
func evidenceTime(d evidenceDocument, key string, now, floor time.Time) (time.Time, error) {
	value, err := time.Parse(time.RFC3339Nano, evidenceText(d, key))
	if err != nil || now.Sub(value) > evidenceMaxAge || value.After(now.Add(5*time.Minute)) || (!floor.IsZero() && value.Before(floor.Add(-5*time.Minute))) {
		return time.Time{}, evidenceFailure(key + " freshness")
	}
	return value, nil
}
func sameEvidenceTime(d evidenceDocument, key string, expected time.Time) bool {
	value, err := time.Parse(time.RFC3339Nano, evidenceText(d, key))
	return err == nil && value.Equal(expected)
}
func evidenceStrings(value any) (map[string]bool, bool) {
	list, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := map[string]bool{}
	for _, item := range list {
		v, ok := item.(string)
		if !ok || v == "" || result[v] {
			return nil, false
		}
		result[v] = true
	}
	return result, true
}
func evidenceCounts(value any) (map[string]int64, bool) {
	object := evidenceObject(value)
	if object == nil {
		return nil, false
	}
	result := map[string]int64{}
	for k, v := range object {
		n, ok := evidenceInteger(v)
		if !ok || n < 0 {
			return nil, false
		}
		result[k] = n
	}
	return result, true
}

// Capsule manifests use Python json.dumps(sort_keys=True,separators=(',',':'))
// with ensure_ascii=True. Numeric manifest fields are integers by contract.
func canonicalCapsuleManifest(value evidenceDocument) ([]byte, error) {
	var normalize func(any) (any, error)
	normalize = func(v any) (any, error) {
		switch item := v.(type) {
		case json.Number:
			n, ok := evidenceInteger(item)
			if !ok {
				return nil, errors.New("non-integer capsule manifest number")
			}
			return n, nil
		case map[string]any:
			out := map[string]any{}
			for k, x := range item {
				n, e := normalize(x)
				if e != nil {
					return nil, e
				}
				out[k] = n
			}
			return out, nil
		case evidenceDocument:
			return normalize(map[string]any(item))
		case []any:
			out := make([]any, len(item))
			for i, x := range item {
				n, e := normalize(x)
				if e != nil {
					return nil, e
				}
				out[i] = n
			}
			return out, nil
		case nil, bool, string, int, int64:
			return item, nil
		default:
			return nil, errors.New("unsupported capsule manifest value")
		}
	}
	normalized, err := normalize(value)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(normalized); err != nil {
		return nil, err
	}
	var ascii strings.Builder
	for _, r := range strings.TrimSuffix(buffer.String(), "\n") {
		if r < 0x7f {
			ascii.WriteRune(r)
		} else if r <= 0xffff {
			fmt.Fprintf(&ascii, "\\u%04x", r)
		} else {
			high, low := utf16.EncodeRune(r)
			fmt.Fprintf(&ascii, "\\u%04x\\u%04x", high, low)
		}
	}
	return []byte(ascii.String()), nil
}

func validateRecoveryEvidence(raw []byte, expected recoveryExpectations, now time.Time) (evidenceDocument, error) {
	r, err := decodeEvidence(raw)
	if err != nil {
		return nil, err
	}
	if !evidenceSnapshotPattern.MatchString(expected.Snapshot) || !evidenceHashPattern.MatchString(expected.FullCiphertextSHA256) || evidenceText(r, "source_snapshot_id") != expected.Snapshot {
		return nil, evidenceFailure("source identity")
	}
	format := evidenceText(r, "format")
	if format != recoveryV1 && format != recoveryV2 {
		return nil, evidenceFailure("format")
	}
	if err = requireEvidence(r, "owner_offline_identity_verified", "isolated_network_internal", "s3_credentials_disposable", "postgres_row_counts_match", "canary_uid_preserved", "message_bytes_match", "attachment_bytes_match", "body_sha256_matches", "restore_succeeded"); err != nil {
		return nil, err
	}
	if r["status"] != "ok" || r["production_data_modified"] != false {
		return nil, evidenceFailure("recovery outcome")
	}
	for _, pair := range [][2]string{{"message_sha256", "message_expected_sha256"}, {"body_sha256", "body_expected_sha256"}, {"attachment_sha256", "attachment_expected_sha256"}} {
		actual := evidenceHash(r, pair[0])
		if actual == "" || actual != evidenceText(r, pair[1]) {
			return nil, evidenceFailure("content checksum")
		}
	}
	if _, err = evidenceTime(r, "verified_at", now, time.Time{}); err != nil {
		return nil, err
	}
	if format == recoveryV1 {
		for _, key := range []string{"capsule_id", "capsule_sha256", "capsule_nas_roundtrip_verified", "all_mail_payload_files_verified"} {
			if _, present := r[key]; present {
				return nil, evidenceFailure("capsule evidence cannot be relabelled v1")
			}
		}
		if evidenceText(r, "ciphertext_sha256") != expected.FullCiphertextSHA256 {
			return nil, evidenceFailure("full ciphertext")
		}
		if err = requireEvidence(r, "nas_roundtrip_verified", "all_snapshot_files_sha256_verified"); err != nil {
			return nil, err
		}
		return r, nil
	}
	if err = validateCapsuleRecovery(r, expected, now); err != nil {
		return nil, err
	}
	return r, nil
}

func validateExportReceipt(receipt evidenceDocument, id, sha string, size int64, completed time.Time) error {
	actualSize, ok := evidenceCount(receipt, "bytes")
	if receipt["format"] != "atoman-encrypted-export-v1" || receipt["ciphertext"] != "backup.tar.age" || evidenceText(receipt, "source_snapshot_id") != id || evidenceHash(receipt, "sha256") != sha || !ok || size <= 0 || actualSize != size || !sameEvidenceTime(receipt, "source_completed_at", completed) {
		return evidenceFailure("export receipt")
	}
	return nil
}
func validateCopyMarker(marker evidenceDocument, id, sha string, size int64, now, completed time.Time) error {
	n, ok := evidenceCount(marker, "bytes")
	if marker["format"] != "atoman-nas-copy-v1" || marker["source"] != id || marker["sha256"] != sha || !ok || n != size || marker["ciphertext_transferred"] != true {
		return evidenceFailure("NAS publication marker")
	}
	if value, present := marker["source_snapshot_id"]; present && value != id {
		return evidenceFailure("NAS marker snapshot")
	}
	if _, present := marker["source_completed_at"]; present && !sameEvidenceTime(marker, "source_completed_at", completed) {
		return evidenceFailure("NAS marker source time")
	}
	if _, err := evidenceTime(marker, "downloaded_at", now, completed); err != nil {
		return err
	}
	return nil
}

func validateCapsuleRecovery(r evidenceDocument, expected recoveryExpectations, now time.Time) error {
	if _, present := r["ciphertext_sha256"]; present {
		return evidenceFailure("ambiguous ciphertext field")
	}
	if _, present := r["nas_roundtrip_verified"]; present {
		return evidenceFailure("ambiguous NAS roundtrip field")
	}
	if r["all_snapshot_files_sha256_verified"] != false {
		return evidenceFailure("capsule cannot claim full snapshot readback")
	}
	if err := requireEvidence(r, "capsule_nas_roundtrip_verified", "all_mail_payload_files_verified"); err != nil {
		return err
	}
	scope := evidenceText(r, "scope")
	if scope != "pre_activation_mail_capsule" && scope != "post_activation_mail_capsule" {
		return evidenceFailure("capsule scope")
	}
	if r["mailbox"] != "INBOX" {
		return evidenceFailure("restored mailbox")
	}
	count, ok := evidenceCount(r, "verified_file_count")
	if !ok || count < 6 {
		return evidenceFailure("complete mail payload")
	}
	manifest, err := decodeEvidence(expected.SourceManifest)
	if err != nil {
		return evidenceFailure("independent source manifest")
	}
	sourceReceipt, err := decodeEvidence(expected.SourceExportReceipt)
	if err != nil {
		return evidenceFailure("independent source export receipt")
	}
	capsuleReceipt, err := decodeEvidence(expected.CapsuleReceipt)
	if err != nil {
		return evidenceFailure("independent capsule receipt")
	}
	if manifest["complete"] != true || manifest["mail_assets_verified"] != true || evidenceHash(r, "source_manifest_sha256") != evidenceDigest(expected.SourceManifest) {
		return evidenceFailure("source manifest checksum or completeness")
	}
	completed, err := evidenceTime(manifest, "completed_at", now, time.Time{})
	if err != nil {
		return err
	}
	if !sameEvidenceTime(r, "source_completed_at", completed) {
		return evidenceFailure("source completion time")
	}
	verified, err := evidenceTime(r, "verified_at", now, completed)
	if err != nil {
		return err
	}
	fullSize, ok := evidenceCount(r, "source_full_ciphertext_bytes")
	if !ok || fullSize <= 0 || evidenceHash(r, "source_full_ciphertext_sha256") != expected.FullCiphertextSHA256 {
		return evidenceFailure("source full ciphertext")
	}
	if err = validateExportReceipt(sourceReceipt, expected.Snapshot, expected.FullCiphertextSHA256, fullSize, completed); err != nil {
		return err
	}
	capsuleID := expected.Snapshot + "m"
	capsuleSHA := evidenceHash(r, "capsule_sha256")
	capsuleSize, ok := evidenceCount(r, "capsule_bytes")
	if r["capsule_id"] != capsuleID || capsuleSHA == "" || capsuleSHA == expected.FullCiphertextSHA256 || !ok || capsuleSize <= 0 {
		return evidenceFailure("capsule identity")
	}
	if err = validateExportReceipt(capsuleReceipt, capsuleID, capsuleSHA, capsuleSize, completed); err != nil {
		return err
	}
	if capsuleReceipt["backup_scope"] != "mail-complete" || capsuleReceipt["paired_source_snapshot_id"] != expected.Snapshot {
		return evidenceFailure("capsule export pairing")
	}

	fullVerified, ok := r["full_snapshot_nas_verified"].(bool)
	if !ok || r["main_bundle_nas_verified"] != fullVerified {
		return evidenceFailure("full snapshot NAS claim")
	}
	mainNAS := evidenceObject(r["main_bundle_nas"])
	mainSize, ok := evidenceCount(mainNAS, "bytes")
	if mainNAS["source_snapshot_id"] != expected.Snapshot || mainNAS["sha256"] != expected.FullCiphertextSHA256 || !ok || mainSize != fullSize || mainNAS["mac_full_ciphertext_readback"] != false {
		return evidenceFailure("main bundle identity or readback claim")
	}
	if fullVerified {
		if mainNAS["verification_method"] != "nas_pull_full_ciphertext_sha256" || mainNAS["nas_path"] != nasSnapshotRoot+expected.Snapshot+"/backup.tar.age" || !reflect.DeepEqual(evidenceObject(mainNAS["receipt"]), sourceReceipt) {
			return evidenceFailure("main NAS verified receipt")
		}
		if err = validateCopyMarker(evidenceObject(mainNAS["copy_marker"]), expected.Snapshot, expected.FullCiphertextSHA256, fullSize, now, completed); err != nil {
			return err
		}
		status := evidenceObject(mainNAS["pull_status"])
		if status["state"] != "ok" || status["source_snapshot_id"] != expected.Snapshot {
			return evidenceFailure("main NAS completed pull")
		}
		stamp, e := evidenceTime(mainNAS, "verified_at", now, completed)
		if e != nil || stamp.After(verified.Add(5*time.Minute)) {
			return evidenceFailure("main NAS verification time")
		}
	} else {
		partial, ok := evidenceCount(mainNAS, "partial_bytes")
		if mainNAS["status"] != "pending" || !ok || partial > fullSize {
			return evidenceFailure("main NAS pending observation")
		}
		for _, key := range []string{"receipt", "copy_marker", "pull_status", "verified_at"} {
			if _, present := mainNAS[key]; present {
				return evidenceFailure("pending main bundle cannot contain success evidence")
			}
		}
		stamp, e := evidenceTime(mainNAS, "observed_at", now, completed)
		if e != nil || stamp.After(verified.Add(5*time.Minute)) {
			return evidenceFailure("main NAS observation time")
		}
	}
	capsuleNAS := evidenceObject(r["capsule_nas"])
	n, nOK := evidenceCount(capsuleNAS, "bytes")
	readback, readbackOK := evidenceCount(capsuleNAS, "readback_bytes")
	if capsuleNAS["capsule_id"] != capsuleID || capsuleNAS["paired_source_snapshot_id"] != expected.Snapshot || capsuleNAS["sha256"] != capsuleSHA || capsuleNAS["readback_sha256"] != capsuleSHA || !nOK || n != capsuleSize || !readbackOK || readback != capsuleSize || capsuleNAS["nas_path"] != nasSnapshotRoot+capsuleID+"/backup.tar.age" || !reflect.DeepEqual(evidenceObject(capsuleNAS["receipt"]), capsuleReceipt) {
		return evidenceFailure("actual capsule NAS roundtrip")
	}
	if err = validateCopyMarker(evidenceObject(capsuleNAS["copy_marker"]), capsuleID, capsuleSHA, capsuleSize, now, completed); err != nil {
		return err
	}
	stamp, err := evidenceTime(capsuleNAS, "verified_at", now, completed)
	if err != nil || stamp.After(verified.Add(5*time.Minute)) {
		return evidenceFailure("capsule NAS verification time")
	}

	capsuleManifest := evidenceObject(r["capsule_manifest"])
	canonical, err := canonicalCapsuleManifest(capsuleManifest)
	manifestSHA := evidenceHash(r, "capsule_manifest_sha256")
	if err != nil || capsuleManifest == nil || manifestSHA == "" || evidenceDigest(canonical) != manifestSHA || capsuleReceipt["capsule_manifest_sha256"] != manifestSHA {
		return evidenceFailure("independently bound capsule manifest")
	}
	if capsuleManifest["format"] != "atoman-mail-recovery-capsule-v1" || capsuleManifest["scope"] != scope || capsuleManifest["capsule_id"] != capsuleID || capsuleManifest["source_snapshot_id"] != expected.Snapshot || !sameEvidenceTime(capsuleManifest, "source_completed_at", completed) {
		return evidenceFailure("capsule manifest source identity")
	}
	for _, key := range []string{"source_manifest_sha256", "source_full_ciphertext_sha256"} {
		if evidenceHash(capsuleManifest, key) == "" || capsuleManifest[key] != r[key] {
			return evidenceFailure("capsule manifest " + key)
		}
	}
	if n, ok := evidenceCount(capsuleManifest, "source_full_ciphertext_bytes"); !ok || n != fullSize {
		return evidenceFailure("capsule manifest source bytes")
	}

	records := map[string]evidenceDocument{}
	backups, ok := manifest["backups"].([]any)
	if !ok || len(backups) != 3 {
		return evidenceFailure("source paired databases")
	}
	for _, value := range backups {
		record := evidenceObject(value)
		name := evidenceText(record, "database")
		if (name != "atoman_prod" && name != "atoman_dev" && name != "atoman_mail") || records[name] != nil || record["restore_verified"] != true || evidenceHash(record, "sha256") == "" || record["dump"] != name+".dump" {
			return evidenceFailure("source database restore")
		}
		profile := evidenceText(record, "restore_profile")
		if profile != "" && profile != "full" {
			if name == "atoman_mail" || profile != "data_without_secondary_indexes" || manifest["verification_profile"] != "data-and-mail" {
				return evidenceFailure("source restore profile")
			}
			reference := evidenceObject(manifest["full_verification_reference"])
			referenceTime, e := time.Parse(time.RFC3339Nano, evidenceText(reference, "completed_at"))
			if !evidenceSnapshotPattern.MatchString(evidenceText(reference, "snapshot_id")) || evidenceHash(reference, "manifest_sha256") == "" || e != nil || referenceTime.After(completed.Add(5*time.Minute)) {
				return evidenceFailure("prior full restore reference")
			}
		}
		records[name] = record
	}
	assetsSHA := ""
	assetsFound := false
	additional, _ := manifest["additional_files"].([]any)
	for _, value := range additional {
		item := evidenceObject(value)
		if item["name"] == "mail-assets.tar" {
			if assetsFound || item["verified"] != true {
				return evidenceFailure("source mail assets")
			}
			assetsFound = true
			assetsSHA = evidenceHash(item, "sha256")
		}
	}
	if assetsSHA == "" {
		return evidenceFailure("source mail assets checksum")
	}
	storage := evidenceObject(r["mail_storage"])
	if err = requireEvidence(storage, "all_r2_objects_restored", "all_runtime_files_verified"); err != nil {
		return err
	}
	for key, want := range map[string]string{"atoman_mail_dump_sha256": evidenceHash(records["atoman_mail"], "sha256"), "mail_assets_sha256": assetsSHA} {
		if evidenceHash(storage, key) != want || capsuleManifest[key] != want {
			return evidenceFailure("mail storage " + key)
		}
	}
	for _, key := range []string{"r2_inventory_sha256", "runtime_files_sha256"} {
		if evidenceHash(storage, key) == "" || capsuleManifest[key] != storage[key] {
			return evidenceFailure("mail storage " + key)
		}
	}
	buckets, ok := evidenceStrings(storage["r2_buckets"])
	if !ok || !reflect.DeepEqual(buckets, map[string]bool{"atoman-mail-prod": true, "atoman-mail-pending": true}) {
		return evidenceFailure("complete private R2 bucket set")
	}
	manifestBuckets, ok := evidenceStrings(capsuleManifest["r2_buckets"])
	if !ok || !reflect.DeepEqual(buckets, manifestBuckets) {
		return evidenceFailure("bound R2 bucket set")
	}
	objectCounts, ok := evidenceCounts(storage["r2_object_counts"])
	manifestCounts, mOK := evidenceCounts(capsuleManifest["r2_object_counts"])
	if !ok || !mOK || len(objectCounts) != 2 || !reflect.DeepEqual(objectCounts, manifestCounts) {
		return evidenceFailure("bound complete R2 inventory")
	}
	total := int64(0)
	for bucket := range buckets {
		n, present := objectCounts[bucket]
		if !present {
			return evidenceFailure("missing R2 bucket inventory")
		}
		total += n
	}
	runtimeCount, ok := evidenceCount(storage, "runtime_file_count")
	manifestRuntime, mOK := evidenceCount(capsuleManifest, "runtime_file_count")
	assetSummary := evidenceObject(r["mail_assets"])
	observedObjects, oOK := evidenceCount(assetSummary, "objects")
	observedRuntime, rOK := evidenceCount(assetSummary, "runtime_files")
	if !ok || !mOK || runtimeCount <= 0 || runtimeCount != manifestRuntime || !oOK || total != observedObjects || total <= 0 || !rOK || runtimeCount != observedRuntime {
		return evidenceFailure("complete restored asset coverage")
	}
	metadata := evidenceObject(r["mail_metadata"])
	if metadata["format"] != "atoman-mail-application-recovery-v1" || metadata["source_database"] != "atoman_prod" || evidenceHash(metadata, "source_dump_sha256") != evidenceHash(records["atoman_prod"], "sha256") || capsuleManifest["atoman_prod_dump_sha256"] != metadata["source_dump_sha256"] {
		return evidenceFailure("paired application mail metadata")
	}
	if err = requireEvidence(metadata, "table_counts_match", "user_links_verified", "credential_account_links_verified", "draft_owner_links_verified", "restore_succeeded"); err != nil {
		return err
	}
	for _, key := range []string{"application_payload_sha256", "application_manifest_sha256"} {
		if evidenceHash(metadata, key) == "" || capsuleManifest[key] != metadata[key] {
			return evidenceFailure("application artifact " + key)
		}
	}
	originalCounts, ok := evidenceCounts(records["atoman_prod"]["public_table_counts"])
	if !ok {
		return evidenceFailure("original application table inventory")
	}
	expectedTables := map[string]int64{}
	for name, count := range originalCounts {
		if name == "Users" || strings.HasPrefix(name, "mail_") {
			expectedTables[name] = count
		}
	}
	if _, present := expectedTables["Users"]; !present {
		return evidenceFailure("original Users ownership mapping")
	}
	sourceTables, sOK := evidenceCounts(metadata["source_tables"])
	restoredTables, rOK := evidenceCounts(metadata["restored_tables"])
	boundTables, bOK := evidenceCounts(capsuleManifest["application_source_tables"])
	if !sOK || !rOK || !bOK || !reflect.DeepEqual(expectedTables, sourceTables) || !reflect.DeepEqual(sourceTables, restoredTables) || !reflect.DeepEqual(sourceTables, boundTables) {
		return evidenceFailure("complete original mail tables and Users")
	}
	absent := map[string]bool{}
	for _, name := range applicationMailTables {
		if _, present := expectedTables[name]; !present {
			absent[name] = true
		}
	}
	observedAbsent, aOK := evidenceStrings(metadata["absent_mail_tables"])
	boundAbsent, bOK := evidenceStrings(capsuleManifest["application_absent_mail_tables"])
	if !aOK || !bOK || !reflect.DeepEqual(absent, observedAbsent) || !reflect.DeepEqual(absent, boundAbsent) {
		return evidenceFailure("honest source mail schema presence")
	}
	if scope == "post_activation_mail_capsule" {
		if metadata["phase"] != "active" || len(absent) != 0 {
			return evidenceFailure("post-activation complete mail schema")
		}
	} else if metadata["phase"] != "pre_activation" {
		return evidenceFailure("pre-activation mail metadata phase")
	}
	return nil
}

func newStalwartPreflightClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("Stalwart preflight redirects are forbidden")
	}}
}

func preflightStalwartAdmin(endpoint, username, secret string) error {
	if endpoint != stalwartPreflightEndpoint {
		return errors.New("unexpected mail service endpoint")
	}
	req, err := http.NewRequest(http.MethodGet, stalwartPreflightEndpoint+"/jmap/session", nil)
	if err != nil {
		return errors.New("invalid fixed mail service endpoint")
	}
	req.SetBasicAuth(username, secret)
	response, err := newStalwartPreflightClient().Do(req)
	if err != nil {
		return errors.New("Stalwart service unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("Stalwart administrator session unavailable")
	}
	return nil
}

func activate(options activationOptions) error {
	receiptPath, snapshot, sha, envPath, adminPath, apply := options.recoveryReport, options.snapshot, options.ciphertextSHA, options.mailEnv, options.adminJSON, options.apply
	if os.Geteuid() != 0 {
		return errors.New("root required")
	}
	raw, e := privateFile(receiptPath)
	if e != nil {
		return e
	}
	expected := recoveryExpectations{Snapshot: snapshot, FullCiphertextSHA256: sha}
	header, e := decodeEvidence(raw)
	if e != nil {
		return e
	}
	if header["format"] == recoveryV2 {
		if expected.SourceManifest, e = evidenceFile(options.sourceManifest); e != nil {
			return errors.New("independent source manifest required")
		}
		if expected.SourceExportReceipt, e = evidenceFile(options.sourceExportReceipt); e != nil {
			return errors.New("independent source export receipt required")
		}
		if expected.CapsuleReceipt, e = evidenceFile(options.capsuleReceipt); e != nil {
			return errors.New("independent capsule export receipt required")
		}
	}
	r, e := validateRecoveryEvidence(raw, expected, time.Now())
	if e != nil {
		return e
	}
	stamp := evidenceText(r, "verified_at")
	canaryRaw, e := privateFile("/var/backups/atoman/mail-canary/canary-20261105.json")
	if e != nil {
		return e
	}
	var canary map[string]any
	if json.Unmarshal(canaryRaw, &canary) != nil || canary["attachment_sha256"] != r["attachment_sha256"] {
		return errors.New("recovered canary differs from source")
	}
	old, e := privateFile(envPath)
	if e != nil {
		return e
	}
	values, e := godotenv.Unmarshal(string(old))
	if e != nil {
		return errors.New("invalid protected mail environment")
	}
	if values["ATOMAN_MAIL_R2_RW_VERIFIED"] != "1" || values["ATOMAN_MAIL_JMAP_ENCRYPTION_KEY_B64"] == "" || values["ATOMAN_MAIL_OUTBOX_ENCRYPTION_KEY_B64"] == "" {
		return errors.New("mail storage preparation incomplete")
	}
	raw, e = privateFile(adminPath)
	if e != nil {
		return e
	}
	var a struct {
		Username string `json:"username"`
		Secret   string `json:"secret"`
	}
	if json.Unmarshal(raw, &a) != nil || a.Username == "" || len(a.Secret) < 20 {
		return errors.New("service provisioning credential unavailable")
	}
	if e := preflightStalwartAdmin(values["ATOMAN_MAIL_STALWART_URL"], a.Username, a.Secret); e != nil {
		return e
	}
	for _, k := range []string{"ATOMAN_MAIL_INVITES_ENABLED", "ATOMAN_MAIL_READ_ENABLED", "ATOMAN_MAIL_SEND_ENABLED", "ATOMAN_MAIL_PROVISION_ENABLED", "ATOMAN_MAIL_STALWART_READY", "ATOMAN_MAIL_TRUENAS_RESTORE_VERIFIED"} {
		values[k] = "1"
	}
	values["ATOMAN_MAIL_STALWART_ADMIN_USER"] = a.Username
	values["ATOMAN_MAIL_STALWART_ADMIN_PASSWORD"] = a.Secret
	values["ATOMAN_MAIL_RECOVERY_SNAPSHOT_ID"] = snapshot
	values["ATOMAN_MAIL_RECOVERY_VERIFIED_AT"] = stamp
	values["ATOMAN_MAIL_RECOVERY_EVIDENCE_FORMAT"] = evidenceText(r, "format")
	values["ATOMAN_MAIL_RECOVERY_FULL_CIPHERTEXT_SHA256"] = sha
	if r["format"] == recoveryV2 {
		values["ATOMAN_MAIL_RECOVERY_EVIDENCE_SCOPE"] = evidenceText(r, "scope")
		values["ATOMAN_MAIL_RECOVERY_CAPSULE_SHA256"] = evidenceText(r, "capsule_sha256")
	} else {
		values["ATOMAN_MAIL_RECOVERY_EVIDENCE_SCOPE"] = "full_snapshot"
		delete(values, "ATOMAN_MAIL_RECOVERY_CAPSULE_SHA256")
	}
	for _, v := range values {
		if strings.ContainsAny(v, "\r\n\x00") {
			return errors.New("multiline environment rejected")
		}
	}
	next, e := godotenv.Marshal(values)
	if e != nil {
		return errors.New("environment serialization failed")
	}
	if !apply {
		fmt.Println("MAIL_ACTIVATION_PREFLIGHT_OK", snapshot)
		return nil
	}
	backup := filepath.Join("/var/lib/atoman-mail", "backend.pre-activation-20261105.env")
	f, e := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e == nil {
		_, e = f.Write(old)
		if e == nil {
			e = f.Sync()
		}
		f.Close()
		if e != nil {
			return e
		}
	} else if !os.IsExist(e) {
		return e
	}
	temp, e := os.CreateTemp(filepath.Dir(envPath), ".backend.env-*")
	if e != nil {
		return e
	}
	name := temp.Name()
	defer os.Remove(name)
	if e = temp.Chmod(0600); e == nil {
		_, e = temp.WriteString(next + "\n")
	}
	if e == nil {
		e = temp.Sync()
	}
	temp.Close()
	if e != nil {
		return e
	}
	if e = os.Rename(name, envPath); e != nil {
		return e
	}
	fmt.Println("MAIL_ACTIVATION_ENVIRONMENT_READY", snapshot)
	return nil
}
func main() {
	var options activationOptions
	flag.StringVar(&options.recoveryReport, "recovery-report", "", "private observed recovery report")
	flag.StringVar(&options.snapshot, "snapshot", "", "exact reviewed original source snapshot")
	flag.StringVar(&options.ciphertextSHA, "ciphertext-sha256", "", "exact reviewed FULL source bundle checksum, also for v2")
	flag.StringVar(&options.sourceManifest, "source-manifest", "", "v2: original immutable source manifest.json")
	flag.StringVar(&options.sourceExportReceipt, "source-export-receipt", "", "v2: independent original FULL bundle receipt.json")
	flag.StringVar(&options.capsuleReceipt, "capsule-receipt", "", "v2: independent capsule export receipt.json")
	flag.StringVar(&options.mailEnv, "mail-env", "/etc/atoman-mail/backend.env", "protected mail environment")
	flag.StringVar(&options.adminJSON, "admin-json", "/etc/atoman-mail/stalwart-admin.json", "protected provisioning credential")
	flag.BoolVar(&options.apply, "apply", false, "write enabled configuration after verification")
	flag.Parse()
	if e := activate(options); e != nil {
		fmt.Println("MAIL_ACTIVATION_FAILED", e)
		os.Exit(1)
	}
}
