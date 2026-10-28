package papersassets

import (
	"context"
	"encoding/json"
	"fmt"
)

// AWSImmutablePapersPolicyJSON emits a REVIEW-ONLY AWS-style reference policy.
// It grants no access. A separate identity-specific policy must grant the
// publisher conditional PutObject, and the root Launcher GetObject only.
//
// WARNING: this document is NOT automatically compatible with MinIO's
// policy engine. Do not install without a fail-closed live policy test.
func AWSImmutablePapersPolicyJSON(bucket string) ([]byte, error) {
	if !bucketNameRE.MatchString(bucket) || bucket == "public" || bucket == "atoman" || bucket == "atoman-public" {
		return nil, ErrInvalid
	}
	prefix := "arn:aws:s3:::" + bucket + "/" + privateRegistryPrefix + "*"
	type statement struct {
		Sid       string                       `json:"Sid"`
		Effect    string                       `json:"Effect"`
		Principal string                       `json:"Principal"`
		Action    any                          `json:"Action"`
		Resource  any                          `json:"Resource"`
		Condition map[string]map[string]string `json:"Condition,omitempty"`
	}
	policy := struct {
		Version   string      `json:"Version"`
		Statement []statement `json:"Statement"`
	}{Version: "2012-10-17", Statement: []statement{
		{Sid: "DenyNonConditionalWrites", Effect: "Deny", Principal: "*",
			Action: "s3:PutObject", Resource: prefix,
			Condition: map[string]map[string]string{
				"Null": {"s3:if-none-match": "true"},
				"Bool": {"s3:ObjectCreationOperation": "true"},
			}},
		{Sid: "DenyAllObjectAndVersionDeletes", Effect: "Deny", Principal: "*",
			Action: []string{"s3:DeleteObject", "s3:DeleteObjectVersion"}, Resource: prefix},
		{Sid: "DenyInsecureTransport", Effect: "Deny", Principal: "*",
			Action:    "s3:*",
			Resource:  []string{"arn:aws:s3:::" + bucket, prefix},
			Condition: map[string]map[string]string{"Bool": {"aws:SecureTransport": "false"}}},
	}}
	raw, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("private bucket policy: %w", err)
	}
	return raw, nil
}

// TrustedBucketPolicyReview is intentionally read-only and cannot mutate S3.
type TrustedBucketPolicyReview interface {
	Review(context.Context, string) (string, error)
}
