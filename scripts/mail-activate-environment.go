//go:build ignore

// Operator-only activation. A dated NAS recovery receipt is required before
// any mail gate is written. Credentials never appear in command output.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/joho/godotenv"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func privateFile(path string) ([]byte, error) {
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private regular file required")
	}
	return os.ReadFile(path)
}
func activate(receiptPath, snapshot, sha, envPath, adminPath string, apply bool) error {
	if os.Geteuid() != 0 {
		return errors.New("root required")
	}
	raw, e := privateFile(receiptPath)
	if e != nil {
		return e
	}
	var r map[string]any
	if json.Unmarshal(raw, &r) != nil {
		return errors.New("invalid recovery receipt")
	}
	if r["format"] != "atoman-mail-nas-recovery-v1" || r["source_snapshot_id"] != snapshot || r["ciphertext_sha256"] != sha || !regexp.MustCompile("^[a-f0-9]{64}$").MatchString(sha) {
		return errors.New("recovery receipt identity mismatch")
	}
	for _, k := range []string{"nas_roundtrip_verified", "owner_offline_identity_verified", "all_snapshot_files_sha256_verified", "isolated_network_internal", "s3_credentials_disposable", "postgres_row_counts_match", "canary_uid_preserved", "message_bytes_match", "attachment_bytes_match"} {
		if r[k] != true {
			return errors.New("recovery evidence incomplete: " + k)
		}
	}
	if r["status"] != "ok" || r["restore_succeeded"] != true || r["body_sha256_matches"] != true {
		return errors.New("recovery acceptance not complete")
	}
	for _, pair := range [][2]string{{"message_sha256", "message_expected_sha256"}, {"body_sha256", "body_expected_sha256"}, {"attachment_sha256", "attachment_expected_sha256"}} {
		actual, ok := r[pair[0]].(string)
		if !ok || !regexp.MustCompile("^[a-f0-9]{64}$").MatchString(actual) || actual != r[pair[1]] {
			return errors.New("recovery content hash mismatch")
		}
	}
	if r["production_data_modified"] != false {
		return errors.New("recovery modified production data")
	}
	stamp, _ := r["verified_at"].(string)
	verified, e := time.Parse(time.RFC3339Nano, stamp)
	if e != nil || time.Since(verified) > 48*time.Hour || verified.After(time.Now().Add(5*time.Minute)) {
		return errors.New("recovery evidence expired")
	}
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
	endpoint := values["ATOMAN_MAIL_STALWART_URL"]
	if endpoint != "http://127.0.0.1:18580" {
		return errors.New("unexpected mail service endpoint")
	}
	req, _ := http.NewRequest(http.MethodGet, endpoint+"/jmap/session", nil)
	req.SetBasicAuth(a.Username, a.Secret)
	response, e := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if e != nil {
		return errors.New("Stalwart service unavailable")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("Stalwart administrator session unavailable")
	}
	for _, k := range []string{"ATOMAN_MAIL_INVITES_ENABLED", "ATOMAN_MAIL_READ_ENABLED", "ATOMAN_MAIL_SEND_ENABLED", "ATOMAN_MAIL_PROVISION_ENABLED", "ATOMAN_MAIL_STALWART_READY", "ATOMAN_MAIL_TRUENAS_RESTORE_VERIFIED"} {
		values[k] = "1"
	}
	values["ATOMAN_MAIL_STALWART_ADMIN_USER"] = a.Username
	values["ATOMAN_MAIL_STALWART_ADMIN_PASSWORD"] = a.Secret
	values["ATOMAN_MAIL_RECOVERY_SNAPSHOT_ID"] = snapshot
	values["ATOMAN_MAIL_RECOVERY_VERIFIED_AT"] = stamp
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
	receipt := flag.String("recovery-report", "", "private observed recovery report")
	snapshot := flag.String("snapshot", "", "exact reviewed source snapshot")
	sha := flag.String("ciphertext-sha256", "", "exact reviewed NAS ciphertext checksum")
	env := flag.String("mail-env", "/etc/atoman-mail/backend.env", "protected mail environment")
	admin := flag.String("admin-json", "/etc/atoman-mail/stalwart-admin.json", "protected provisioning credential")
	apply := flag.Bool("apply", false, "write enabled configuration after verification")
	flag.Parse()
	if e := activate(*receipt, *snapshot, *sha, *env, *admin, *apply); e != nil {
		fmt.Println("MAIL_ACTIVATION_FAILED", e)
		os.Exit(1)
	}
}
