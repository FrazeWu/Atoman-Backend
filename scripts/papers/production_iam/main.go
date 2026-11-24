package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	madmin "github.com/minio/madmin-go/v3"
)

type Credential struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}
type Entry struct {
	Role       string     `json:"role"`
	Credential Credential `json:"credential"`
	Prefixes   []string   `json:"prefixes"`
	Write      bool       `json:"write"`
}
type Plan struct {
	Entries []Entry `json:"entries"`
}

func fail(s string) { fmt.Fprintln(os.Stderr, s); os.Exit(1) }
func main() {
	if os.Geteuid() != 0 || os.Getenv("PAPERS_PRODUCTION_STORAGE_CONFIRM") != "1" || len(os.Args) != 1 {
		fail("explicit production storage authorization required")
	}
	var plan Plan
	if json.NewDecoder(os.Stdin).Decode(&plan) != nil || len(plan.Entries) != 6 {
		fail("invalid private storage plan")
	}
	const bucket = "atoman-papers-private-v1"
	adm, err := madmin.New("127.0.0.1:9100", os.Getenv("PAPERS_ADMIN_ACCESS"), os.Getenv("PAPERS_ADMIN_SECRET"), false)
	if err != nil {
		fail("storage admin init failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	policies, err := adm.ListCannedPolicies(ctx)
	if err != nil {
		fail("policy inventory failed")
	}
	users, err := adm.ListUsers(ctx)
	if err != nil {
		fail("user inventory failed")
	}
	expected := map[string]bool{"snapshot-writer": true, "pdf-writer": true, "environment-writer": true, "api-reader": true, "launcher-reader": true, "environment-reader": true}
	for _, e := range plan.Entries {
		if !expected[e.Role] || len(e.Credential.AccessKey) < 16 || len(e.Credential.SecretKey) < 32 || len(e.Prefixes) < 1 {
			fail("invalid identity")
		}
		delete(expected, e.Role)
		if _, ok := policies["atoman-papers-v1-"+e.Role]; ok {
			fail("policy collision")
		}
		if _, ok := users[e.Credential.AccessKey]; ok {
			fail("identity collision")
		}
	}
	if len(expected) != 0 {
		fail("missing identity")
	}
	for _, e := range plan.Entries {
		var resources []string
		for _, p := range e.Prefixes {
			switch p {
			case "_probes", "snapshots", "pdf-results", "environments", "trusted-releases", "trusted-archives":
			default:
				fail("unknown storage prefix")
			}
			resources = append(resources, "arn:aws:s3:::"+bucket+"/papers/immutable/v1/"+p+"/*")
		}
		actions := []string{"s3:GetObject"}
		if e.Write {
			actions = append(actions, "s3:PutObject")
		}
		data, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": []map[string]any{
			{"Effect": "Allow", "Action": actions, "Resource": resources},
			{"Effect": "Allow", "Action": []string{"s3:GetBucketVersioning", "s3:GetBucketObjectLockConfiguration"}, "Resource": []string{"arn:aws:s3:::" + bucket}},
		}})
		if err != nil {
			fail("policy encoding failed")
		}
		name := "atoman-papers-v1-" + e.Role
		if adm.AddCannedPolicy(ctx, name, data) != nil {
			fail("policy creation failed: " + e.Role)
		}
		if adm.AddUser(ctx, e.Credential.AccessKey, e.Credential.SecretKey) != nil {
			fail("identity creation failed: " + e.Role)
		}
		if adm.SetPolicy(ctx, name, e.Credential.AccessKey, false) != nil {
			fail("policy binding failed: " + e.Role)
		}
		fmt.Println("PRIVATE_ROLE_BOUND", e.Role)
	}
	const size = uint64(12) << 30
	if adm.SetBucketQuota(ctx, bucket, &madmin.BucketQuota{Quota: size, Size: size, Type: madmin.HardQuota}) != nil {
		fail("quota configuration failed")
	}
	quota, err := adm.GetBucketQuota(ctx, bucket)
	if err != nil || quota.Type != madmin.HardQuota || (quota.Size != size && quota.Quota != size) {
		fail("quota verification failed")
	}
	fmt.Println("PRIVATE_STORAGE_HARD_QUOTA_VERIFIED", size)
}
