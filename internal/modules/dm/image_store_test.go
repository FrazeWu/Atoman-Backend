package dm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
)

func TestUploadImageSniffsBytesAndStoresPrivateObject(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&model.DMImage{}); err != nil {
		t.Fatal(err)
	}
	actor := testUser(t, db)
	store := &memoryImageStore{local: true}
	service := NewService(NewRepo(db), store, nil, nil)

	image, err := service.UploadImage(context.Background(), authctx.CurrentUser{ID: actor}, bytes.NewReader(validDMPNG()), "image/png", int64(len(validDMPNG())))
	if err != nil {
		t.Fatalf("upload image: %v", err)
	}
	if image.URL != "/api/v1/dm/images/"+image.ID.String()+"/content" {
		t.Fatalf("unexpected local URL: %q", image.URL)
	}
	if len(store.objects) != 1 {
		t.Fatalf("expected one private object, got %d", len(store.objects))
	}
	for key := range store.objects {
		if key != "images/"+actor.String()+"/"+image.ID.String()+".png" {
			t.Fatalf("unexpected object key: %q", key)
		}
	}
}

func TestUploadImageRejectsSpoofedType(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&model.DMImage{}); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepo(db), &memoryImageStore{local: true}, nil, nil)
	_, err := service.UploadImage(context.Background(), authctx.CurrentUser{ID: testUser(t, db)}, bytes.NewReader([]byte("not an image")), "image/png", int64(len("not an image")))
	if !errors.Is(err, ErrImageInvalid) {
		t.Fatalf("expected invalid image, got %v", err)
	}
}

func TestSendBindsOnlyOwnedUnusedImage(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&model.DMImage{}); err != nil {
		t.Fatal(err)
	}
	actor, recipient, stranger := testUser(t, db), testUser(t, db), testUser(t, db)
	store := &memoryImageStore{local: true}
	service := NewService(NewRepo(db), store, nil, nil)
	image, err := service.UploadImage(context.Background(), authctx.CurrentUser{ID: actor}, bytes.NewReader(validDMPNG()), "image/png", int64(len(validDMPNG())))
	if err != nil {
		t.Fatal(err)
	}
	input := SendInput{ImageID: &image.ID, ClientMessageID: uuid.New()}
	message, err := service.SendToTarget(context.Background(), actor, TargetRef{Type: model.DMPartyUser, ID: recipient}, input)
	if err != nil || message.ImageID == nil || *message.ImageID != image.ID {
		t.Fatalf("expected bound image, got %#v %v", message, err)
	}
	if _, err := service.SendInConversation(context.Background(), recipient, message.ConversationID, SendInput{Content: "reply", ClientMessageID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SendToTarget(context.Background(), actor, TargetRef{Type: model.DMPartyUser, ID: recipient}, SendInput{ImageID: &image.ID, ClientMessageID: uuid.New()}); !errors.Is(err, ErrImageInvalid) {
		t.Fatalf("expected reuse rejection, got %v", err)
	}
	if _, err := service.SendToTarget(context.Background(), stranger, TargetRef{Type: model.DMPartyUser, ID: recipient}, SendInput{ImageID: &image.ID, ClientMessageID: uuid.New()}); !errors.Is(err, ErrImageInvalid) {
		t.Fatalf("expected foreign image rejection, got %v", err)
	}
}

func TestOpenImageAllowsOwnerOrConversationParticipant(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&model.DMImage{}); err != nil {
		t.Fatal(err)
	}
	actor, recipient, stranger := testUser(t, db), testUser(t, db), testUser(t, db)
	store := &memoryImageStore{local: true}
	service := NewService(NewRepo(db), store, nil, nil)
	image, err := service.UploadImage(context.Background(), authctx.CurrentUser{ID: actor}, bytes.NewReader(validDMPNG()), "image/png", int64(len(validDMPNG())))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SendToTarget(context.Background(), actor, TargetRef{Type: model.DMPartyUser, ID: recipient}, SendInput{ImageID: &image.ID, ClientMessageID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	if body, _, err := service.OpenImage(context.Background(), authctx.CurrentUser{ID: recipient}, image.ID); err != nil {
		t.Fatalf("recipient read: %v", err)
	} else {
		_ = body.Close()
	}
	if _, _, err := service.OpenImage(context.Background(), authctx.CurrentUser{ID: stranger}, image.ID); !errors.Is(err, ErrConversationForbidden) {
		t.Fatalf("expected stranger denial, got %v", err)
	}
}

type memoryImageStore struct {
	local   bool
	objects map[string][]byte
}

func (s *memoryImageStore) Put(_ context.Context, key, _ string, body io.Reader, _ int64) error {
	if s.objects == nil {
		s.objects = map[string][]byte{}
	}
	data, err := io.ReadAll(body)
	if err == nil {
		s.objects[key] = data
	}
	return err
}

func (s *memoryImageStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, errors.New("missing")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *memoryImageStore) SignedURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://private.test/" + key, nil
}
func (s *memoryImageStore) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}
func (s *memoryImageStore) IsLocal() bool { return s.local }

func validDMPNG() []byte { return []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a} }
