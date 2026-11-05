package main

import (
	"atoman/internal/modules/mail"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/joho/godotenv"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	app := flag.String("app-env", "", "explicit dotenv input; never shell source")
	pg := flag.String("pg-env", "/etc/atoman-mail/postgres.env", "dedicated PostgreSQL secret")
	out := flag.String("output", "/etc/atoman-mail", "protected configuration directory")
	probe := flag.Bool("verify-objects", false, "probe disposable objects in both mail buckets")
	write := flag.Bool("write-config", false, "prepare service configuration, without activating mail")
	flag.Parse()
	if err := run(*app, *pg, *out, *probe, *write); err != nil {
		fmt.Fprintln(os.Stderr, "mail preparation failed:", err)
		os.Exit(1)
	}
}
func privateWrite(path string, b []byte) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	c := f.Close()
	if e != nil {
		return e
	}
	return c
}
func envBytes(v map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(v))
	for k, s := range v {
		if strings.ContainsAny(s, "\r\n\x00") {
			return nil, errors.New("multiline environment value rejected")
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, v[k])
	}
	return []byte(b.String()), nil
}
func run(app, pg, out string, probe, write bool) error {
	if app == "" {
		return errors.New("--app-env is required")
	}
	v, e := godotenv.Read(app)
	if e != nil {
		return errors.New("cannot parse application dotenv")
	}
	cfg, e := mail.LoadR2Config(func(k string) string { return v[k] })
	if e != nil {
		return e
	}
	c, e := cfg.NewS3Client()
	if e != nil {
		return e
	}
	if e = mail.VerifyBuckets(c, cfg); e != nil {
		return e
	}
	fmt.Println("MAIL_PRIVATE_R2_HEAD_VERIFIED")
	if probe {
		if e = mail.ProbePrivateMailObjects(c, cfg); e != nil {
			return e
		}
		fmt.Println("MAIL_PRIVATE_R2_PUT_GET_DELETE_INTEGRITY_VERIFIED")
	}
	if !write {
		return nil
	}
	if !probe {
		return errors.New("--write-config requires --verify-objects")
	}
	if os.Geteuid() != 0 {
		return errors.New("root required")
	}
	p, e := godotenv.Read(pg)
	if e != nil || p["STALWART_PG_PASSWORD"] == "" {
		return errors.New("mail PostgreSQL secret unavailable")
	}
	if s, e := os.Lstat(out); e == nil && (s.Mode()&os.ModeSymlink != 0 || !s.IsDir()) {
		return errors.New("unsafe output directory")
	}
	if e = os.MkdirAll(out, 0700); e != nil {
		return e
	}
	if e = os.Chmod(out, 0700); e != nil {
		return e
	}
	for _, n := range []string{"stalwart.env", "bootstrap.env", "bootstrap-request.json", "backend.env"} {
		if _, e := os.Lstat(filepath.Join(out, n)); e == nil {
			return fmt.Errorf("refusing to overwrite %s", n)
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	key1, key2, bootstrap := make([]byte, 32), make([]byte, 32), make([]byte, 32)
	for _, b := range [][]byte{key1, key2, bootstrap} {
		if _, e = rand.Read(b); e != nil {
			return e
		}
	}
	files := map[string]map[string]string{
		"stalwart.env":  {"STALWART_PG_PASSWORD": p["STALWART_PG_PASSWORD"], "AWS_ACCESS_KEY_ID": cfg.AccessKeyID, "AWS_SECRET_ACCESS_KEY": cfg.SecretKey, "STALWART_PUBLIC_URL": "https://mail.atoman.org"},
		"bootstrap.env": {"STALWART_RECOVERY_ADMIN": "admin:" + hex.EncodeToString(bootstrap)},
		"backend.env":   {"ATOMAN_MAIL_R2_ACCESS_KEY_ID": cfg.AccessKeyID, "ATOMAN_MAIL_R2_SECRET_ACCESS_KEY": cfg.SecretKey, "ATOMAN_MAIL_STALWART_URL": "http://127.0.0.1:18580", "ATOMAN_MAIL_JMAP_ENCRYPTION_KEY_B64": base64.StdEncoding.EncodeToString(key1), "ATOMAN_MAIL_OUTBOX_ENCRYPTION_KEY_B64": base64.StdEncoding.EncodeToString(key2), "MAIL_R2_BLOB_BUCKET": cfg.BlobBucket, "MAIL_R2_PENDING_BUCKET": cfg.PendingBucket, "ATOMAN_MAIL_R2_RW_VERIFIED": "1"},
	}
	for n, v := range files {
		b, e := envBytes(v)
		if e != nil {
			return e
		}
		if e = privateWrite(filepath.Join(out, n), b); e != nil {
			return e
		}
	}
	config := map[string]any{
		"defaultHostname": "mail.atoman.org", "defaultDomain": "atoman.org", "requestTlsCertificate": false, "generateDkimKeys": true, "dnsServer": map[string]any{"@type": "Manual"},
		"dataStore":   map[string]any{"@type": "PostgreSql", "host": "atoman-dev-postgres-1", "port": 5432, "database": "atoman_mail", "authUsername": "atoman_mail", "authSecret": map[string]string{"@type": "EnvironmentVariable", "variableName": "STALWART_PG_PASSWORD"}, "poolMaxConnections": 8},
		"blobStore":   map[string]any{"@type": "S3", "bucket": cfg.BlobBucket, "region": map[string]string{"@type": "Custom", "url": cfg.Endpoint, "region": cfg.Region}, "accessKeyId": map[string]string{"@type": "EnvironmentVariable", "variableName": "AWS_ACCESS_KEY_ID"}, "secretKey": map[string]string{"@type": "EnvironmentVariable", "variableName": "AWS_SECRET_ACCESS_KEY"}, "sessionToken": map[string]string{"@type": "None"}},
		"searchStore": map[string]string{"@type": "Default"}, "directory": map[string]string{"@type": "Internal"},
	}
	b, e := json.MarshalIndent(map[string]any{"using": []string{"urn:ietf:params:jmap:core", "urn:stalwart:jmap"}, "methodCalls": []any{[]any{"x:Bootstrap/set", map[string]any{"update": map[string]any{"singleton": config}}, "bootstrap"}}}, "", "  ")
	if e != nil {
		return e
	}
	if e = privateWrite(filepath.Join(out, "bootstrap-request.json"), b); e != nil {
		return e
	}
	fmt.Println("MAIL_SERVICE_CONFIG_PREPARED_WITH_FEATURE_GATES_CLOSED")
	return nil
}
