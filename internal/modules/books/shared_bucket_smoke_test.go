package books

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"atoman/internal/storage"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/stretchr/testify/require"
)

// 显式指定环境文件才连接真实 R2；只创建并清理自己的临时对象，不写数据库。
func TestBookSharedBucketStorageSmoke(t *testing.T) {
	file := os.Getenv("BOOK_R2_SMOKE_ENV_FILE")
	if file == "" {
		t.Skip("set BOOK_R2_SMOKE_ENV_FILE to verify a real protected R2 bucket")
	}
	config, err := godotenv.Read(file)
	require.NoError(t, err)
	for _, name := range []string{"S3_ENDPOINT", "S3_REGION", "S3_BUCKET", "S3_URL_PREFIX", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
		t.Setenv(name, config[name])
	}
	t.Setenv("BOOK_PRIVATE_BUCKET", "")
	t.Setenv("BOOK_SHARED_BUCKET_PREFIX_PROTECTED", "true")
	client, err := storage.InitS3Client()
	require.NoError(t, err)
	store := newPrivateBookResourceStore(client)
	require.NotNil(t, store)
	key := storage.BuildBookRevisionObjectKey("storage-probe-"+uuid.NewString(), "txt")
	body := "Atoman book storage connectivity check."
	upload, err := store.CreateMultipartUpload(key, "text/plain")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.AbortMultipartUpload(key, upload) })
	t.Cleanup(func() { require.NoError(t, store.DeleteObject(key)) })
	signed, err := store.PresignUploadPart(key, upload, 1, 5*time.Minute)
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPut, signed, strings.NewReader(body))
	require.NoError(t, err)
	httpClient := &http.Client{Timeout: 30 * time.Second}
	response, err := httpClient.Do(request)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, response.Body)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)
	etag := response.Header.Get("ETag")
	require.NotEmpty(t, etag)
	require.NoError(t, store.CompleteMultipartUpload(key, upload, []BookUploadPart{{PartNumber: 1, ETag: etag, Size: int64(len(body))}}))
	object, err := store.OpenObject(key)
	require.NoError(t, err)
	actual, err := io.ReadAll(object)
	require.NoError(t, object.Close())
	require.NoError(t, err)
	require.Equal(t, body, string(actual))
	response, err = httpClient.Get(strings.TrimRight(config["S3_URL_PREFIX"], "/") + "/" + key)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, response.Body)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusForbidden, response.StatusCode, "真实对象也不能绕过扫描和下架控制")
}
