package mail

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/stretchr/testify/require"
)

type fakeR2Probe struct {
	objects       map[string][]byte
	buckets       map[string]bool
	puts, deletes int
	corrupt       bool
}

func newFakeR2Probe() *fakeR2Probe {
	return &fakeR2Probe{objects: map[string][]byte{}, buckets: map[string]bool{}}
}
func (f *fakeR2Probe) id(bucket, key *string) string {
	b := aws.StringValue(bucket)
	f.buckets[b] = true
	return b + "/" + aws.StringValue(key)
}
func (f *fakeR2Probe) PutObject(v *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
	f.puts++
	data, _ := io.ReadAll(v.Body)
	f.objects[f.id(v.Bucket, v.Key)] = data
	return &s3.PutObjectOutput{}, nil
}
func (f *fakeR2Probe) GetObject(v *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
	data := f.objects[f.id(v.Bucket, v.Key)]
	if f.corrupt {
		data = []byte("tampered")
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(data))}, nil
}
func (f *fakeR2Probe) DeleteObject(v *s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error) {
	f.deletes++
	delete(f.objects, f.id(v.Bucket, v.Key))
	return &s3.DeleteObjectOutput{}, nil
}
func (f *fakeR2Probe) HeadObject(v *s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
	if _, ok := f.objects[f.id(v.Bucket, v.Key)]; ok {
		return &s3.HeadObjectOutput{}, nil
	}
	return nil, awserr.New("NotFound", "missing", nil)
}
func TestMailPrivateR2ProbeUsesOnlyMailBucketsAndCleansUp(t *testing.T) {
	cfg, err := LoadR2Config(fromMap(validR2Env()))
	require.NoError(t, err)
	fake := newFakeR2Probe()
	require.NoError(t, ProbePrivateMailObjects(fake, cfg))
	require.Equal(t, 2, fake.puts)
	require.Equal(t, 2, fake.deletes)
	require.Empty(t, fake.objects)
	require.Equal(t, map[string]bool{DefaultMailBlobBucket: true, DefaultMailPendingBucket: true}, fake.buckets)
	require.False(t, fake.buckets["atoman"])
}
func TestMailPrivateR2ProbeCleansUpAfterReadMismatch(t *testing.T) {
	cfg, err := LoadR2Config(fromMap(validR2Env()))
	require.NoError(t, err)
	fake := newFakeR2Probe()
	fake.corrupt = true
	require.ErrorContains(t, ProbePrivateMailObjects(fake, cfg), "integrity")
	require.Empty(t, fake.objects)
	require.Equal(t, 1, fake.deletes)
}
func TestLiveExistingMailR2ObjectsOnlyWithExplicitOperatorOptIn(t *testing.T) {
	if os.Getenv("ATOMAN_MAIL_R2_RW_INTEGRATION") != "1" {
		t.Skip("requires operator-supplied existing R2 runtime environment")
	}
	cfg, err := LoadR2ConfigFromEnv()
	require.NoError(t, err)
	client, err := cfg.NewS3Client()
	require.NoError(t, err)
	require.NoError(t, ProbePrivateMailObjects(client, cfg))
	t.Log("Existing Atoman R2 key validated Put/Get/Delete in both private mail buckets")
}
