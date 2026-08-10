package storage

import (
	"net/url"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
)

type PromotedMusicAsset struct {
	URL            string
	SourceKey      string
	DestinationKey string
}

func PromoteMusicUploadAsset(client *s3.S3, rawURL, destinationKey string) (PromotedMusicAsset, error) {
	result := PromotedMusicAsset{URL: strings.TrimSpace(rawURL)}
	if client == nil || !strings.EqualFold(strings.TrimSpace(os.Getenv("STORAGE_TYPE")), "s3") {
		return result, nil
	}
	prefix := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	if prefix == "" || bucket == "" || !strings.HasPrefix(result.URL, prefix+"/") {
		return result, nil
	}
	sourceKey, err := url.PathUnescape(strings.TrimPrefix(result.URL, prefix+"/"))
	if err != nil {
		return result, nil
	}
	sourceKey = strings.TrimLeft(sourceKey, "/")
	if !strings.HasPrefix(sourceKey, "music/covers/uploads/") && !strings.HasPrefix(sourceKey, "music/audio/uploads/") {
		return result, nil
	}
	escapedSource := strings.ReplaceAll(url.PathEscape(bucket+"/"+sourceKey), "%2F", "/")
	if _, err := client.CopyObject(&s3.CopyObjectInput{
		Bucket: aws.String(bucket), CopySource: aws.String(escapedSource), Key: aws.String(destinationKey),
	}); err != nil {
		return PromotedMusicAsset{}, err
	}
	return PromotedMusicAsset{
		URL: prefix + "/" + destinationKey, SourceKey: sourceKey, DestinationKey: destinationKey,
	}, nil
}

func DeleteMusicObjects(client *s3.S3, keys []string) {
	if client == nil {
		return
	}
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	seen := map[string]bool{}
	for _, key := range keys {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		_, _ = client.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	}
}
