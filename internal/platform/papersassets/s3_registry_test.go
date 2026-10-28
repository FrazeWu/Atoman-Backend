package papersassets

import (
	"context"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeConditional struct {
	mu                 sync.Mutex
	objects            map[string][]byte
	ignoreConditional  bool
	denyReads          bool
	versioningDisabled bool
	publicRead         bool
	putFailure         error
	lostAckOnce        bool
	puts               atomic.Int64
}

func newFakeConditional() *fakeConditional {
	return &fakeConditional{objects: make(map[string][]byte)}
}
func (f *fakeConditional) VersioningEnabled(_ context.Context, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.versioningDisabled, nil
}
func (f *fakeConditional) AnonymousReadDenied(_ context.Context, _, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.publicRead, nil
}
func (f *fakeConditional) PutIfAbsent(_ context.Context, bucket, key string, raw []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts.Add(1)
	target := bucket + "/" + key
	if f.putFailure != nil {
		return f.putFailure
	}
	if _, ok := f.objects[target]; ok && !f.ignoreConditional {
		return ErrAlreadyExists
	}
	f.objects[target] = append([]byte(nil), raw...)
	if f.lostAckOnce {
		f.lostAckOnce = false
		return awserr.New("RequestError", "simulated response lost after server commit", nil)
	}
	return nil
}
func (f *fakeConditional) Read(_ context.Context, bucket, key string, max int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.denyReads {
		return nil, errors.New("permission denied")
	}
	value, ok := f.objects[bucket+"/"+key]
	if !ok {
		return nil, errors.New("missing object")
	}
	if int64(len(value)) > max {
		return nil, ErrIntegrity
	}
	return append([]byte(nil), value...), nil
}
func testRegistry(t *testing.T) (*PrivateS3Registry, *fakeConditional) {
	t.Helper()
	api := newFakeConditional()
	reg, err := NewPrivateS3Registry(api, "papers-private-test", "atoman-public")
	require.NoError(t, err)
	require.NoError(t, reg.Probe(context.Background()))
	return reg, api
}
func TestS3RegistryMustProbeBeforeWriting(t *testing.T) {
	api := newFakeConditional()
	reg, err := NewPrivateS3Registry(api, "papers-private-test", "atoman-public")
	require.NoError(t, err)
	_, err = reg.PublishSnapshot(context.Background(), fixtureSnapshot())
	require.ErrorIs(t, err, ErrConditionalUnsupported)
	require.Zero(t, api.puts.Load())
}
func TestS3RegistryRejectsProviderThatIgnoresConditionalHeader(t *testing.T) {
	api := newFakeConditional()
	api.ignoreConditional = true
	reg, err := NewPrivateS3Registry(api, "papers-private-test", "atoman-public")
	require.NoError(t, err)
	require.ErrorIs(t, reg.Probe(context.Background()), ErrConditionalUnsupported)
	_, err = reg.PublishSnapshot(context.Background(), fixtureSnapshot())
	require.ErrorIs(t, err, ErrConditionalUnsupported)
}
func TestS3RegistryConcurrentIdempotentSameSnapshot(t *testing.T) {
	reg, api := testRegistry(t)
	snap := fixtureSnapshot()
	const workers = 32
	var wg sync.WaitGroup
	ids := make(chan string, workers)
	failures := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dig, err := reg.PublishSnapshot(context.Background(), snap)
			if err != nil {
				failures <- err
				return
			}
			ids <- dig
		}()
	}
	wg.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	found := map[string]bool{}
	for dig := range ids {
		found[dig] = true
	}
	require.Len(t, found, 1)
	for dig := range found {
		loaded, err := reg.LoadSnapshot(context.Background(), dig)
		require.NoError(t, err)
		require.Equal(t, snap.ProjectID, loaded.ProjectID)
		require.Equal(t, snap.Revision, loaded.Revision)
		require.Len(t, loaded.Files, len(snap.Files))
	}
	api.mu.Lock()
	objectCount := len(api.objects)
	api.mu.Unlock()
	require.Equal(t, 2, objectCount, "one probe + one canonical snapshot")
}

func TestS3RegistryRejectsPoisonedExistingKey(t *testing.T) {
	reg, api := testRegistry(t)
	snap := fixtureSnapshot()
	dig, err := reg.PublishSnapshot(context.Background(), snap)
	require.NoError(t, err)
	key, err := reg.objectKey("snapshots", dig)
	require.NoError(t, err)
	api.mu.Lock()
	api.objects[reg.bucket+"/"+key] = []byte("injected")
	api.mu.Unlock()
	_, err = reg.LoadSnapshot(context.Background(), dig)
	require.ErrorIs(t, err, ErrIntegrity)
	_, err = reg.PublishSnapshot(context.Background(), snap)
	require.ErrorIs(t, err, ErrIntegrity)
}
func TestS3RegistryVersionAndPrivateBucketValidation(t *testing.T) {
	api := newFakeConditional()
	for _, test := range []struct{ bucket, shared string }{
		{"public", "different"}, {"atoman-public", "atoman-public"},
		{"../../../escape", "atoman-public"}, {"UPPERCASE", "atoman-public"},
		{"x", "atoman-public"}, {"bad bucket", "atoman-public"},
	} {
		_, err := NewPrivateS3Registry(api, test.bucket, test.shared)
		require.ErrorIs(t, err, ErrInvalid, fmt.Sprintf("%s", test.bucket))
	}
	reg, _ := testRegistry(t)
	_, err := reg.objectKey("user-upload", "sha256:"+fmt.Sprintf("%064x", 1))
	require.ErrorIs(t, err, ErrInvalid)
	_, err = reg.objectKey("snapshots", "../wrong")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = reg.PublishEnvironment(context.Background(), Environment{Version: 1, Name: "unapproved"})
	require.ErrorIs(t, err, ErrInvalid)
}
func TestS3RegistryReadFailureBlocksPublish(t *testing.T) {
	reg, api := testRegistry(t)
	api.mu.Lock()
	api.denyReads = true
	api.mu.Unlock()
	_, err := reg.PublishSnapshot(context.Background(), fixtureSnapshot())
	require.Error(t, err, "no publication without verified readback")
}
func TestS3RegistryReadbackPreservesCanonicalOrdering(t *testing.T) {
	reg, _ := testRegistry(t)
	snap := fixtureSnapshot()
	swapped := snap
	swapped.Files = append([]SourceFile(nil), snap.Files...)
	swapped.Files[0], swapped.Files[1] = swapped.Files[1], swapped.Files[0]
	first, err := reg.PublishSnapshot(context.Background(), snap)
	require.NoError(t, err)
	second, err := reg.PublishSnapshot(context.Background(), swapped)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, "sections/test.tex", snap.Files[0].Path, "original input remains unchanged")
	loaded, err := reg.LoadSnapshot(context.Background(), first)
	require.NoError(t, err)
	require.Equal(t, "main.tex", loaded.Files[0].Path)
}
func TestS3RegistryEnvironmentRoundtrip(t *testing.T) {
	reg, _ := testRegistry(t)
	env := testEnv()
	digest, err := reg.PublishEnvironment(context.Background(), env)
	require.NoError(t, err)
	loaded, err := reg.LoadEnvironment(context.Background(), digest)
	require.NoError(t, err)
	require.Equal(t, env, loaded)
}

func TestS3RegistryProbeRefusesPublicOrUnversionedBucket(t *testing.T) {
	for _, scenario := range []string{"public", "unversioned"} {
		api := newFakeConditional()
		if scenario == "public" {
			api.publicRead = true
		} else {
			api.versioningDisabled = true
		}
		registry, err := NewPrivateS3Registry(api, "papers-private-test", "atoman-shared")
		require.NoError(t, err)
		require.ErrorIs(t, registry.Probe(context.Background()), ErrConditionalUnsupported)
		_, err = registry.PublishSnapshot(context.Background(), fixtureSnapshot())
		require.ErrorIs(t, err, ErrConditionalUnsupported)
	}
}

func TestS3RegistryNoAuthorizationBypassViaReadback(t *testing.T) {
	reg, api := testRegistry(t)
	snap := fixtureSnapshot()
	_, err := reg.PublishSnapshot(context.Background(), snap)
	require.NoError(t, err)
	denied := errors.New("write permission denied")
	api.mu.Lock()
	api.putFailure = denied
	api.mu.Unlock()
	_, err = reg.PublishSnapshot(context.Background(), snap)
	require.ErrorIs(t, err, denied, "readable matching content must not hide PutObject AccessDenied")
}
func TestS3RegistryLostAcknowledgementIsVerifiedNotOverwritten(t *testing.T) {
	reg, api := testRegistry(t)
	api.mu.Lock()
	api.lostAckOnce = true
	api.mu.Unlock()
	snap := fixtureSnapshot()
	id, err := reg.PublishSnapshot(context.Background(), snap)
	require.NoError(t, err)
	loaded, err := reg.LoadSnapshot(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, snap.ProjectID, loaded.ProjectID)
}
func TestS3RegistryCompileAssetInterfaceValidatesArchive(t *testing.T) {
	reg, _ := testRegistry(t)
	env := testEnv()
	archive := filepath.Join(t.TempDir(), "rootfs.tar")
	require.NoError(t, os.WriteFile(archive, []byte("sandbox fixture rootfs archive"), 0600))
	digest, err := reg.PublishEnvironment(context.Background(), env)
	require.NoError(t, err)
	checked, err := reg.VerifyCompileArchive(context.Background(), digest, archive)
	require.NoError(t, err)
	require.Equal(t, env, checked)
	require.NoError(t, os.WriteFile(archive, []byte("corrupted"), 0600))
	_, err = reg.VerifyCompileArchive(context.Background(), digest, archive)
	require.ErrorIs(t, err, ErrIntegrity)
	linked := filepath.Join(t.TempDir(), "symlink.tar")
	require.NoError(t, os.Symlink(archive, linked))
	_, err = reg.VerifyCompileArchive(context.Background(), digest, linked)
	require.ErrorIs(t, err, ErrInvalid)
}
