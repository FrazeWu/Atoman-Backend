package comment

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestReplyToChildStaysInTwoLevelThread(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "root", nil)
	child := ctx.create(t, 1, "child", &root.ID)
	reply := ctx.create(t, 2, "reply child", &child.ID)

	require.Nil(t, root.RootID)
	require.Nil(t, root.ReplyToID)
	require.NotNil(t, root.FloorNumber)
	require.Equal(t, 1, *root.FloorNumber)
	require.Equal(t, root.ID, *child.RootID)
	require.Equal(t, root.ID, *child.ReplyToID)
	require.Nil(t, child.FloorNumber)
	require.Equal(t, root.ID, *reply.RootID)
	require.Equal(t, child.ID, *reply.ReplyToID)
	require.Nil(t, reply.FloorNumber)
}

func TestCreateConcurrentRootsAssignsContinuousFloors(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	start := make(chan struct{})
	results := make(chan CommentDTO, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			created, err := ctx.service.Create(ctx.users[index], ctx.target, CreateCommentInput{Content: fmt.Sprintf("root-%d", index)})
			results <- created
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	floors := make([]int, 0, 2)
	for created := range results {
		require.NotNil(t, created.FloorNumber)
		floors = append(floors, *created.FloorNumber)
	}
	sort.Ints(floors)
	require.Equal(t, []int{1, 2}, floors)

	var target model.DiscussionTarget
	require.NoError(t, ctx.db.First(&target).Error)
	require.Equal(t, 3, target.NextFloor)
}

func TestCreateSerializationIsEnabledOnlyForSQLite(t *testing.T) {
	mutex := createTransactionMutex("sqlite")
	require.NotNil(t, mutex)
	require.Nil(t, createTransactionMutex("postgres"))
	require.NoError(t, withCreateTransactionMutex(mutex, func() error {
		require.False(t, mutex.TryLock())
		return nil
	}))
	require.True(t, mutex.TryLock())
	mutex.Unlock()
}

func TestCreateExtensionFailureRollsBackFloorAndRelations(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindVideo, 0)
	mention := MentionInput{UserID: ctx.users[1].ID, Start: 0, End: len([]rune("@comment-user-1"))}
	asset := createImageAsset(t, ctx.db, ctx.users[0].ID, "image/png")
	writerErr := errors.New("extension rejected")

	_, err := ctx.service.CreateWithExtension(ctx.users[0], ctx.target, CreateCommentInput{
		Content:       "@comment-user-1 1:20",
		Mentions:      []MentionInput{mention},
		AttachmentIDs: []uuid.UUID{asset.ID},
	}, func(tx *gorm.DB, comment *model.CommentEntry) error {
		var count int64
		require.NoError(t, tx.Model(&model.CommentEntry{}).Where("id = ?", comment.ID).Count(&count).Error)
		require.EqualValues(t, 1, count)
		return writerErr
	})
	require.ErrorIs(t, err, writerErr)

	for _, table := range []any{&model.CommentEntry{}, &model.CommentMention{}, &model.CommentAttachment{}, &model.CommentTimeAnchor{}} {
		var count int64
		require.NoError(t, ctx.db.Model(table).Count(&count).Error)
		require.Zero(t, count)
	}
	created := ctx.create(t, 0, "after rollback", nil)
	require.Equal(t, 1, *created.FloorNumber)
}

func TestCreateWithExtensionCommitsCommentAndExtensionTogether(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	type extension struct {
		CommentID uuid.UUID `gorm:"primaryKey"`
		Value     string
	}
	require.NoError(t, ctx.db.AutoMigrate(&extension{}))

	created, err := ctx.service.CreateWithExtension(ctx.users[0], ctx.target, CreateCommentInput{Content: "with extension"}, func(tx *gorm.DB, comment *model.CommentEntry) error {
		var stored model.CommentEntry
		if err := tx.First(&stored, "id = ?", comment.ID).Error; err != nil {
			return err
		}
		return tx.Create(&extension{CommentID: comment.ID, Value: "ok"}).Error
	})
	require.NoError(t, err)
	var stored extension
	require.NoError(t, ctx.db.First(&stored, "comment_id = ?", created.ID).Error)
	require.Equal(t, "ok", stored.Value)
}

func TestCreateValidatesAuthenticationTargetAndReply(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	_, err := ctx.service.Create(authctx.CurrentUser{}, ctx.target, CreateCommentInput{Content: "hello"})
	require.ErrorIs(t, err, ErrAuthenticationRequired)

	invisibleRegistry := &TargetRegistry{resolvers: map[string]TargetResolver{
		TargetKindBlogPost: targetResolverFunc(func(Viewer, uuid.UUID) (ResolvedTarget, error) {
			return ResolvedTarget{Kind: TargetKindBlogPost, ResourceKey: uuid.NewString(), Visible: false}, nil
		}),
	}}
	_, err = NewService(ctx.db, invisibleRegistry).Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "hello"})
	require.ErrorIs(t, err, ErrTargetNotVisible)

	otherID := uuid.New()
	primaryResolver := ctx.service.registry.resolvers[TargetKindBlogPost]
	ctx.service.registry.resolvers[TargetKindBlogPost] = targetResolverFunc(func(viewer Viewer, id uuid.UUID) (ResolvedTarget, error) {
		if id == otherID {
			return ResolvedTarget{Kind: TargetKindBlogPost, ResourceID: otherID, ResourceKey: otherID.String(), Visible: true}, nil
		}
		return primaryResolver.Resolve(viewer, id)
	})
	foreign, err := ctx.service.Create(ctx.users[0], TargetRef{Kind: TargetKindBlogPost, ResourceID: otherID}, CreateCommentInput{Content: "foreign"})
	require.NoError(t, err)
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "cross target", ReplyToID: &foreign.ID})
	require.ErrorIs(t, err, ErrInvalidReply)

	root := ctx.create(t, 0, "deleted root", nil)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", root.ID).Update("status", "deleted").Error)
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "reply", ReplyToID: &root.ID})
	require.ErrorIs(t, err, ErrInvalidReply)
	missing := uuid.New()
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "reply", ReplyToID: &missing})
	require.ErrorIs(t, err, ErrInvalidReply)
}

func TestCreateReplyToChildRejectsCorruptRoot(t *testing.T) {
	cases := map[string]func(t *testing.T, ctx commentTestContext, root model.CommentEntry){
		"missing": func(t *testing.T, ctx commentTestContext, root model.CommentEntry) {
			require.NoError(t, ctx.db.Unscoped().Delete(&model.CommentEntry{}, "id = ?", root.ID).Error)
		},
		"hidden": func(t *testing.T, ctx commentTestContext, root model.CommentEntry) {
			require.NoError(t, ctx.db.Model(&root).Update("status", "hidden").Error)
		},
		"other target": func(t *testing.T, ctx commentTestContext, root model.CommentEntry) {
			otherID := uuid.New()
			other := model.DiscussionTarget{Kind: TargetKindBlogPost, ResourceID: otherID, ResourceKey: otherID.String(), NextFloor: 1}
			require.NoError(t, ctx.db.Create(&other).Error)
			require.NoError(t, ctx.db.Model(&root).Update("target_id", other.ID).Error)
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
			rootDTO := ctx.create(t, 0, "root", nil)
			child := ctx.create(t, 1, "child", &rootDTO.ID)
			var root model.CommentEntry
			require.NoError(t, ctx.db.First(&root, "id = ?", rootDTO.ID).Error)
			corrupt(t, ctx, root)
			var before int64
			require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Count(&before).Error)

			_, err := ctx.service.Create(ctx.users[2], ctx.target, CreateCommentInput{Content: "reply", ReplyToID: &child.ID})
			require.ErrorIs(t, err, ErrInvalidReply)
			var after int64
			require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Count(&after).Error)
			require.Equal(t, before, after)
		})
	}
}

func TestCreateValidatesContentAndAttachments(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	_, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{})
	require.ErrorIs(t, err, ErrInvalidContent)
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: strings.Repeat("界", 2001)})
	require.ErrorIs(t, err, ErrInvalidContent)
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "- list"})
	require.ErrorIs(t, err, ErrInvalidContent)

	valid := createImageAsset(t, ctx.db, ctx.users[0].ID, "image/jpeg")
	pureImage, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{AttachmentIDs: []uuid.UUID{valid.ID}})
	require.NoError(t, err)
	require.Empty(t, pureImage.Content)

	second := createImageAsset(t, ctx.db, ctx.users[0].ID, "image/webp")
	withText, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "image", AttachmentIDs: []uuid.UUID{second.ID}})
	require.NoError(t, err)
	require.Len(t, withText.Attachments, 1)

	tooMany := make([]uuid.UUID, 5)
	for i := range tooMany {
		tooMany[i] = createImageAsset(t, ctx.db, ctx.users[0].ID, "image/png").ID
	}
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{AttachmentIDs: tooMany})
	require.ErrorIs(t, err, ErrInvalidAttachment)
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{AttachmentIDs: []uuid.UUID{valid.ID, valid.ID}})
	require.ErrorIs(t, err, ErrInvalidAttachment)

	cases := []model.MediaAsset{
		{UserID: &ctx.users[0].ID, Purpose: "comment", URL: "x", Key: "x", ContentType: "audio/mpeg"},
		{UserID: &ctx.users[1].ID, Purpose: "comment", URL: "x", Key: "x", ContentType: "image/png"},
		{UserID: &ctx.users[0].ID, Purpose: "comment", ContentType: "image/png"},
	}
	for i := range cases {
		require.NoError(t, ctx.db.Create(&cases[i]).Error)
		_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{AttachmentIDs: []uuid.UUID{cases[i].ID}})
		require.ErrorIs(t, err, ErrInvalidAttachment)
	}
	missing := uuid.New()
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{AttachmentIDs: []uuid.UUID{missing}})
	require.ErrorIs(t, err, ErrInvalidAttachment)
}

func TestCreateValidatesAttachmentSize(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	zero := createImageAsset(t, ctx.db, ctx.users[0].ID, "image/png")
	require.NoError(t, ctx.db.Model(&zero).Update("size", 0).Error)
	_, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{AttachmentIDs: []uuid.UUID{zero.ID}})
	require.ErrorIs(t, err, ErrInvalidAttachment)

	tooLarge := createImageAsset(t, ctx.db, ctx.users[0].ID, "image/png")
	require.NoError(t, ctx.db.Model(&tooLarge).Update("size", 10*1024*1024+1).Error)
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{AttachmentIDs: []uuid.UUID{tooLarge.ID}})
	require.ErrorIs(t, err, ErrInvalidAttachment)

	boundary := createImageAsset(t, ctx.db, ctx.users[0].ID, "image/png")
	require.NoError(t, ctx.db.Model(&boundary).Update("size", 10*1024*1024).Error)
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{AttachmentIDs: []uuid.UUID{boundary.ID}})
	require.NoError(t, err)
}

func TestCreateRejectsNonCommentImageAttachmentPurpose(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	asset := createImageAsset(t, ctx.db, ctx.users[0].ID, "image/png")
	require.NoError(t, ctx.db.Model(&asset).Update("purpose", "music.cover").Error)
	_, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{AttachmentIDs: []uuid.UUID{asset.ID}})
	require.ErrorIs(t, err, ErrInvalidAttachment)
}

func TestCommentDTOIncludesAuthorsAndPersistsReplyIdentity(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "root author", nil)
	child := ctx.create(t, 1, "child author", &root.ID)
	nested := ctx.create(t, 2, "nested author", &child.ID)
	direct := ctx.create(t, 3, "direct author", &root.ID)

	listed, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1})
	require.NoError(t, err)
	require.Equal(t, ctx.users[0].ID, listed.Items[0].Author.ID)
	require.Nil(t, listed.Items[0].ReplyToAuthor)
	require.Equal(t, ctx.users[1].ID, listed.Items[0].Replies[0].Author.ID)
	require.Equal(t, ctx.users[0].ID, listed.Items[0].Replies[0].ReplyToAuthor.ID)
	require.Equal(t, ctx.users[2].ID, listed.Items[0].Replies[1].Author.ID)
	require.Equal(t, ctx.users[1].ID, listed.Items[0].Replies[1].ReplyToAuthor.ID)
	require.Equal(t, ctx.users[3].ID, listed.Items[0].Replies[2].Author.ID)
	require.Equal(t, ctx.users[0].ID, listed.Items[0].Replies[2].ReplyToAuthor.ID)

	var stored model.CommentEntry
	require.NoError(t, ctx.db.First(&stored, "id = ?", nested.ID).Error)
	require.NotNil(t, stored.ReplyToAuthorID)
	require.Equal(t, ctx.users[1].ID, *stored.ReplyToAuthorID)

	page, err := ctx.service.ListReplies(Viewer{}, root.ID, 2, 1)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, nested.ID, page.Items[0].ID)
	require.Equal(t, ctx.users[1].ID, page.Items[0].ReplyToAuthor.ID)

	require.NoError(t, ctx.db.Unscoped().Delete(&model.CommentEntry{}, "id = ?", child.ID).Error)
	page, err = ctx.service.ListReplies(Viewer{}, root.ID, 1, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.Equal(t, nested.ID, page.Items[0].ID)
	require.Equal(t, ctx.users[1].ID, page.Items[0].ReplyToAuthor.ID)
	require.Equal(t, direct.ID, page.Items[1].ID)
	require.NoError(t, ctx.db.Unscoped().Delete(&model.User{}, "uuid = ?", ctx.users[1].ID).Error)
	page, err = ctx.service.ListReplies(Viewer{}, root.ID, 1, 20)
	require.NoError(t, err)
	require.Equal(t, ctx.users[1].ID, page.Items[0].ReplyToAuthor.ID)
	require.Empty(t, page.Items[0].ReplyToAuthor.Username)
}

func TestAttachmentValidationLocksInStableOrderAndReturnsInputOrder(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	aID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	bID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	for _, id := range []uuid.UUID{aID, bID} {
		asset := model.MediaAsset{
			Base: model.Base{ID: id}, UserID: &ctx.users[0].ID, Purpose: "comment.image",
			URL: "https://assets.example/" + id.String(), Key: "comments/" + id.String(), ContentType: "image/png", Size: 128,
		}
		require.NoError(t, ctx.db.Create(&asset).Error)
	}

	lockedIDs := make([]uuid.UUID, 0, 4)
	callbackName := "attachment-lock-order-" + uuid.NewString()
	require.NoError(t, ctx.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "media_assets" {
			return
		}
		for _, value := range tx.Statement.Vars {
			if id, ok := value.(uuid.UUID); ok {
				lockedIDs = append(lockedIDs, id)
				return
			}
		}
	}))
	t.Cleanup(func() { _ = ctx.db.Callback().Query().Remove(callbackName) })

	ba, err := ctx.service.validateAttachments(ctx.db, ctx.users[0].ID, []uuid.UUID{bID, aID})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{bID, aID}, []uuid.UUID{ba[0].ID, ba[1].ID})
	ab, err := ctx.service.validateAttachments(ctx.db, ctx.users[0].ID, []uuid.UUID{aID, bID})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{aID, bID}, []uuid.UUID{ab[0].ID, ab[1].ID})
	require.Equal(t, []uuid.UUID{aID, bID, aID, bID}, lockedIDs)
}

func TestMentionValidationUsesSortedUniqueUserIDs(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	a := model.User{UUID: uuid.MustParse("00000000-0000-0000-0000-000000000011"), Username: "mention-a", Email: "mention-a@example.com", Password: "hash", IsActive: true}
	b := model.User{UUID: uuid.MustParse("00000000-0000-0000-0000-000000000012"), Username: "mention-b", Email: "mention-b@example.com", Password: "hash", IsActive: true}
	require.NoError(t, ctx.db.Create(&a).Error)
	require.NoError(t, ctx.db.Create(&b).Error)
	content := "@mention-b @mention-a"
	secondStart := len([]rune("@mention-b "))
	mentions := []MentionInput{
		{UserID: b.UUID, Start: 0, End: len([]rune("@mention-b"))},
		{UserID: a.UUID, Start: secondStart, End: secondStart + len([]rune("@mention-a"))},
	}
	require.Equal(t, []uuid.UUID{a.UUID, b.UUID}, sortedUUIDs([]uuid.UUID{b.UUID, a.UUID}))
	require.NoError(t, ctx.service.validateMentions(ctx.db, content, mentions))
}

func TestCreateValidatesAndPersistsMentionOccurrences(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	content := "@comment-user-1 and @comment-user-1"
	secondStart := len([]rune("@comment-user-1 and "))
	created, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{
		Content: content,
		Mentions: []MentionInput{
			{UserID: ctx.users[1].ID, Start: 0, End: len([]rune("@comment-user-1"))},
			{UserID: ctx.users[1].ID, Start: secondStart, End: secondStart + len([]rune("@comment-user-1"))},
		},
	})
	require.NoError(t, err)
	require.Len(t, created.Mentions, 2)

	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "bad", Mentions: []MentionInput{{UserID: ctx.users[1].ID, Start: 0, End: 3}}})
	require.ErrorIs(t, err, ErrInvalidMention)
	missing := uuid.New()
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "@missing", Mentions: []MentionInput{{UserID: missing, Start: 0, End: 8}}})
	require.ErrorIs(t, err, ErrInvalidMention)
	require.NoError(t, ctx.db.Model(&model.User{}).Where("uuid = ?", ctx.users[1].ID).Update("is_active", false).Error)
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "@comment-user-1", Mentions: []MentionInput{{UserID: ctx.users[1].ID, Start: 0, End: len([]rune("@comment-user-1"))}}})
	require.ErrorIs(t, err, ErrInvalidMention)
}

func TestCreateRejectsMentionUsernameSpoofing(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	content := "@comment-user-2"
	_, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{
		Content:  content,
		Mentions: []MentionInput{{UserID: ctx.users[1].ID, Start: 0, End: len([]rune(content))}},
	})
	require.ErrorIs(t, err, ErrInvalidMention)
}

func TestCreateMentionOffsetsReferToNFCNormalizedContent(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	mentioned := model.User{Username: "café", Email: "cafe@example.com", Password: "hash", IsActive: true}
	require.NoError(t, ctx.db.Create(&mentioned).Error)
	created, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{
		Content:  "@cafe\u0301",
		Mentions: []MentionInput{{UserID: mentioned.UUID, Start: 0, End: len([]rune("@café"))}},
	})
	require.NoError(t, err)
	require.Equal(t, "@café", created.Content)
}

func TestCreateContentLimitCountsNFCRunes(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	created, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: strings.Repeat("e\u0301", 2000)})
	require.NoError(t, err)
	require.Len(t, []rune(created.Content), 2000)
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: strings.Repeat("e\u0301", 2001)})
	require.ErrorIs(t, err, ErrInvalidContent)
}

func TestCreatePersistsContentHashFromNormalizedContentAndOrderedAttachments(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	a := createImageAsset(t, ctx.db, ctx.users[0].ID, "image/png")
	b := createImageAsset(t, ctx.db, ctx.users[0].ID, "image/png")
	one, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "  hello\r\nworld  ", AttachmentIDs: []uuid.UUID{a.ID, b.ID}})
	require.NoError(t, err)
	var relations []model.CommentAttachment
	require.NoError(t, ctx.db.Where("comment_id = ?", one.ID).Order("position ASC").Find(&relations).Error)
	require.Equal(t, []int{0, 1}, []int{relations[0].Position, relations[1].Position})
	require.Equal(t, []uuid.UUID{a.ID, b.ID}, []uuid.UUID{relations[0].MediaAssetID, relations[1].MediaAssetID})
	two, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "hello\nworld", AttachmentIDs: []uuid.UUID{a.ID, b.ID}})
	require.NoError(t, err)
	three, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "hello\nworld", AttachmentIDs: []uuid.UUID{b.ID, a.ID}})
	require.NoError(t, err)
	var stored []model.CommentEntry
	require.NoError(t, ctx.db.Where("id IN ?", []uuid.UUID{one.ID, two.ID, three.ID}).Order("created_at ASC").Find(&stored).Error)
	require.NotEmpty(t, stored[0].ContentHash)
	require.Equal(t, stored[0].ContentHash, stored[1].ContentHash)
	require.NotEqual(t, stored[0].ContentHash, stored[2].ContentHash)
	require.Equal(t, ContentHash("café", nil), ContentHash("cafe\u0301", nil))
}

func TestCreatePersistsTimeAnchorsOnlyForMediaKinds(t *testing.T) {
	for _, kind := range []string{TargetKindVideo, TargetKindPodcastEpisode, TargetKindMusicSong} {
		t.Run(kind, func(t *testing.T) {
			ctx := newCommentTestContext(t, kind, 0)
			created := ctx.create(t, 0, "0:12 and 1:02:03", nil)
			require.Len(t, created.TimeAnchors, 2)
			require.Equal(t, []int{12, 3723}, []int{created.TimeAnchors[0].Seconds, created.TimeAnchors[1].Seconds})
		})
	}
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	created := ctx.create(t, 0, "0:12", nil)
	require.Empty(t, created.TimeAnchors)
}

func TestListPaginatesRootsAndPreviewsEarliestActiveChildren(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	var first CommentDTO
	for i := 0; i < 22; i++ {
		created := ctx.create(t, i%len(ctx.users), fmt.Sprintf("root-%02d", i+1), nil)
		if i == 0 {
			first = created
		}
	}
	children := make([]CommentDTO, 5)
	for i := range children {
		children[i] = ctx.create(t, (i+1)%len(ctx.users), fmt.Sprintf("child-%d", i+1), &first.ID)
	}
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", children[1].ID).Update("status", "deleted").Error)

	page, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, Sort: SortOldest})
	require.NoError(t, err)
	require.Len(t, page.Items, 20)
	require.Equal(t, 1, *page.Items[0].FloorNumber)
	require.Len(t, page.Items[0].Replies, 3)
	require.Equal(t, []string{"child-1", "child-3", "child-4"}, []string{page.Items[0].Replies[0].Content, page.Items[0].Replies[1].Content, page.Items[0].Replies[2].Content})
	require.Equal(t, 22, page.TotalRoots)
	require.Equal(t, 26, page.TotalComments)
	require.Equal(t, 4, page.TotalReplies)
	require.Equal(t, 20, page.PerPage)

	latest, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, Sort: SortNewest})
	require.NoError(t, err)
	require.Equal(t, 22, *latest.Items[0].FloorNumber)
	_, err = ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 0, Sort: SortOldest})
	require.ErrorIs(t, err, ErrInvalidListOptions)
	_, err = ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, Sort: "random"})
	require.ErrorIs(t, err, ErrInvalidListOptions)
	_, err = ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, Sort: "latest"})
	require.ErrorIs(t, err, ErrInvalidListOptions)
}

func TestListSortsByHotScoreAndPrependsMarkedRootWithoutDuplicates(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	first := ctx.create(t, 0, "first", nil)
	marked := ctx.create(t, 0, "marked", nil)
	hottest := ctx.create(t, 0, "hottest", nil)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", first.ID).Update("hot_score", 5).Error)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", hottest.ID).Update("hot_score", 20).Error)
	var target model.DiscussionTarget
	require.NoError(t, ctx.db.First(&target).Error)
	require.NoError(t, ctx.db.Model(&target).Update("pinned_comment_id", marked.ID).Error)

	listed, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, Sort: SortHot})
	require.NoError(t, err)
	require.Len(t, listed.Items, 3)
	require.Equal(t, marked.ID, listed.Items[0].ID)
	require.True(t, listed.Items[0].Marked)
	require.Equal(t, hottest.ID, listed.Items[1].ID)
	seen := map[uuid.UUID]bool{}
	for _, item := range listed.Items {
		require.False(t, seen[item.ID])
		seen[item.ID] = true
	}
}

func TestListRequiresVisibleTarget(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	ctx.resolved.Visible = false
	ctx.service.registry.resolvers[TargetKindBlogPost] = targetResolverFunc(func(Viewer, uuid.UUID) (ResolvedTarget, error) {
		return ctx.resolved, nil
	})
	_, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, Sort: SortOldest})
	require.ErrorIs(t, err, ErrTargetNotVisible)
}

func TestCreateUpdatesTargetAndRootCounters(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "root", nil)
	ctx.create(t, 1, "child", &root.ID)
	var target model.DiscussionTarget
	require.NoError(t, ctx.db.First(&target).Error)
	require.Equal(t, 2, target.CommentCount)
	require.Equal(t, 1, target.RootCount)
	var stored model.CommentEntry
	require.NoError(t, ctx.db.First(&stored, "id = ?", root.ID).Error)
	require.Equal(t, 1, stored.ReplyCount)
}

func TestContentHashIsStable(t *testing.T) {
	attachments := []uuid.UUID{uuid.MustParse("00000000-0000-0000-0000-000000000001"), uuid.MustParse("00000000-0000-0000-0000-000000000002")}
	require.Equal(t, ContentHash("hello", attachments), ContentHash("hello", attachments))
	require.NotEqual(t, ContentHash("hello", attachments), ContentHash("hello", []uuid.UUID{attachments[1], attachments[0]}))
}

func TestCreateReturnsSafeRenderedHTML(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	created := ctx.create(t, 0, "**bold** [site](https://example.com)", nil)
	require.Contains(t, created.RenderedHTML, "<strong>bold</strong>")
	require.NotContains(t, created.RenderedHTML, "javascript:")
}

func TestListExcludesHiddenAndDeletedComments(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	active := ctx.create(t, 0, "active", nil)
	hidden := ctx.create(t, 0, "hidden", nil)
	deleted := ctx.create(t, 0, "deleted", nil)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", hidden.ID).Update("status", "hidden").Error)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", deleted.ID).Update("status", "deleted").Error)
	listed, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, Sort: SortOldest})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	require.Equal(t, active.ID, listed.Items[0].ID)
}

func TestExtensionWriterReceivesTransaction(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	called := false
	_, err := ctx.service.CreateWithExtension(ctx.users[0], ctx.target, CreateCommentInput{Content: "extension"}, func(tx *gorm.DB, comment *model.CommentEntry) error {
		called = true
		return tx.Model(comment).Update("hot_score", 7).Error
	})
	require.NoError(t, err)
	require.True(t, called)
}

func TestCreateRejectsMissingTarget(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	_, err := ctx.service.Create(ctx.users[0], TargetRef{Kind: TargetKindBlogPost, ResourceID: uuid.New()}, CreateCommentInput{Content: "missing"})
	require.ErrorIs(t, err, ErrTargetNotFound)
}

func TestCreateKeepsErrorIdentityFromExtension(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	want := errors.New("writer failure")
	_, err := ctx.service.CreateWithExtension(ctx.users[0], ctx.target, CreateCommentInput{Content: "failure"}, func(*gorm.DB, *model.CommentEntry) error { return want })
	require.ErrorIs(t, err, want)
}

func TestListMarkedRootConsumesOneSlotOnFirstPage(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	marked := ctx.create(t, 0, "marked", nil)
	for i := 0; i < 20; i++ {
		ctx.create(t, i%len(ctx.users), fmt.Sprintf("ordinary-%d", i), nil)
	}
	var target model.DiscussionTarget
	require.NoError(t, ctx.db.First(&target).Error)
	require.NoError(t, ctx.db.Model(&target).Update("pinned_comment_id", marked.ID).Error)
	listed, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, Sort: SortOldest})
	require.NoError(t, err)
	require.Len(t, listed.Items, 20)
	require.Equal(t, marked.ID, listed.Items[0].ID)
}

func TestListMarkedRootPaginationIsBoundedCompleteAndUnique(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	marked := ctx.create(t, 0, "marked", nil)
	want := map[uuid.UUID]bool{marked.ID: true}
	for i := 0; i < 41; i++ {
		root := ctx.create(t, i%len(ctx.users), fmt.Sprintf("ordinary-%02d", i), nil)
		want[root.ID] = true
	}
	var target model.DiscussionTarget
	require.NoError(t, ctx.db.First(&target).Error)
	require.NoError(t, ctx.db.Model(&target).Update("pinned_comment_id", marked.ID).Error)

	seen := map[uuid.UUID]bool{}
	for pageNumber, wantCount := range []int{20, 20, 2} {
		page, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: pageNumber + 1, Sort: SortOldest})
		require.NoError(t, err)
		require.Len(t, page.Items, wantCount)
		require.LessOrEqual(t, len(page.Items), 20)
		require.Equal(t, 42, page.TotalRoots)
		require.Equal(t, 42, page.TotalComments)
		require.Zero(t, page.TotalReplies)
		require.Equal(t, 20, page.PerPage)
		for index, item := range page.Items {
			require.False(t, seen[item.ID], "comment %s repeated across pages", item.ID)
			seen[item.ID] = true
			if item.ID == marked.ID {
				require.Equal(t, 0, pageNumber)
				require.Equal(t, 0, index)
			}
		}
	}
	require.Equal(t, want, seen)
}

func TestListCustomPageSizeWithMarkedRootIsCompleteAndUnique(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	marked := ctx.create(t, 0, "marked-small-page", nil)
	want := map[uuid.UUID]bool{marked.ID: true}
	for i := 0; i < 11; i++ {
		root := ctx.create(t, i%len(ctx.users), fmt.Sprintf("small-page-%02d", i), nil)
		want[root.ID] = true
	}
	var target model.DiscussionTarget
	require.NoError(t, ctx.db.First(&target).Error)
	require.NoError(t, ctx.db.Model(&target).Update("pinned_comment_id", marked.ID).Error)

	seen := map[uuid.UUID]bool{}
	for pageNumber, wantCount := range []int{5, 5, 2} {
		page, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: pageNumber + 1, PageSize: 5, Sort: SortOldest})
		require.NoError(t, err)
		require.Equal(t, 5, page.PerPage)
		require.Len(t, page.Items, wantCount)
		for _, item := range page.Items {
			require.False(t, seen[item.ID], "comment %s repeated", item.ID)
			seen[item.ID] = true
		}
	}
	require.Equal(t, want, seen)

	first, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, PageSize: 1})
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.Equal(t, []uuid.UUID{marked.ID}, []uuid.UUID{first.Items[0].ID})
	second, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 2, PageSize: 1})
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	require.NotEqual(t, marked.ID, second.Items[0].ID)

	_, err = ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, PageSize: 21})
	require.ErrorIs(t, err, ErrInvalidListOptions)
}

func TestCommentListsRejectOverflowingPagination(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "overflow", nil)
	_, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: math.MaxInt, PageSize: 20})
	require.ErrorIs(t, err, ErrInvalidListOptions)
	_, err = ctx.service.ListReplies(Viewer{}, root.ID, math.MaxInt, 50)
	require.ErrorIs(t, err, ErrInvalidListOptions)
	moderator := ctx.users[0]
	moderator.Role = authctx.RoleModerator
	_, err = ctx.service.ListReports(moderator, "", math.MaxInt, 50)
	require.ErrorIs(t, err, ErrInvalidListOptions)
}

func TestListInvalidPinnedReferenceFallsBackToOrdinaryPagination(t *testing.T) {
	cases := map[string]func(t *testing.T, ctx commentTestContext) uuid.UUID{
		"missing": func(_ *testing.T, _ commentTestContext) uuid.UUID {
			return uuid.New()
		},
		"child": func(t *testing.T, ctx commentTestContext) uuid.UUID {
			root := ctx.create(t, 0, "child-parent", nil)
			return ctx.create(t, 1, "child", &root.ID).ID
		},
		"other target": func(t *testing.T, ctx commentTestContext) uuid.UUID {
			otherID := uuid.New()
			primaryResolver := ctx.service.registry.resolvers[TargetKindBlogPost]
			ctx.service.registry.resolvers[TargetKindBlogPost] = targetResolverFunc(func(viewer Viewer, id uuid.UUID) (ResolvedTarget, error) {
				if id == otherID {
					return ResolvedTarget{Kind: TargetKindBlogPost, ResourceID: otherID, ResourceKey: otherID.String(), Visible: true}, nil
				}
				return primaryResolver.Resolve(viewer, id)
			})
			created, err := ctx.service.Create(ctx.users[0], TargetRef{Kind: TargetKindBlogPost, ResourceID: otherID}, CreateCommentInput{Content: "other-target"})
			require.NoError(t, err)
			return created.ID
		},
		"hidden": func(t *testing.T, ctx commentTestContext) uuid.UUID {
			root := ctx.create(t, 0, "hidden-pinned", nil)
			require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id = ?", root.ID).Update("status", "hidden").Error)
			return root.ID
		},
	}

	for name, badPinned := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
			pinnedID := badPinned(t, ctx)
			ordinary := make([]uuid.UUID, 20)
			for i := range ordinary {
				ordinary[i] = ctx.create(t, i%len(ctx.users), fmt.Sprintf("ordinary-%02d", i), nil).ID
			}
			var target model.DiscussionTarget
			require.NoError(t, ctx.db.Where("kind = ? AND resource_key = ?", ctx.resolved.Kind, ctx.resolved.ResourceKey).First(&target).Error)
			require.NoError(t, ctx.db.Model(&target).Update("pinned_comment_id", pinnedID).Error)

			listed, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, Sort: SortNewest})
			require.NoError(t, err)
			require.Len(t, listed.Items, 20)
			for _, item := range listed.Items {
				require.False(t, item.Marked)
			}
			seen := make(map[uuid.UUID]bool, len(listed.Items))
			for _, item := range listed.Items {
				seen[item.ID] = true
			}
			for _, id := range ordinary {
				require.True(t, seen[id], "ordinary root %s was lost", id)
			}
		})
	}
}

func TestCreateRejectsInactiveAuthor(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	require.NoError(t, ctx.db.Model(&model.User{}).Where("uuid = ?", ctx.users[0].ID).Update("is_active", false).Error)
	_, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "inactive"})
	require.ErrorIs(t, err, ErrAuthenticationRequired)
}

func TestListHotUsesPersistedScoreThenCreationOrder(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	one := ctx.create(t, 0, "one", nil)
	two := ctx.create(t, 0, "two", nil)
	stamp := time.Now().Add(-time.Hour)
	require.NoError(t, ctx.db.Model(&model.CommentEntry{}).Where("id IN ?", []uuid.UUID{one.ID, two.ID}).Updates(map[string]any{"hot_score": 10, "created_at": stamp}).Error)
	listed, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, Sort: SortHot})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{one.ID, two.ID}, []uuid.UUID{listed.Items[0].ID, listed.Items[1].ID})
}

func TestListDefaultSortIsOldest(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	ctx.create(t, 0, "one", nil)
	ctx.create(t, 0, "two", nil)
	listed, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1})
	require.NoError(t, err)
	require.Equal(t, 1, *listed.Items[0].FloorNumber)
}

func TestCreateRejectsUnsupportedImageSubtype(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	asset := createImageAsset(t, ctx.db, ctx.users[0].ID, "image/svg+xml")
	_, err := ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{AttachmentIDs: []uuid.UUID{asset.ID}})
	require.ErrorIs(t, err, ErrInvalidAttachment)
}

func TestListReturnsReplyToSpecificChild(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "root", nil)
	child := ctx.create(t, 1, "child", &root.ID)
	ctx.create(t, 2, "nested", &child.ID)
	listed, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1})
	require.NoError(t, err)
	require.Equal(t, child.ID, *listed.Items[0].Replies[1].ReplyToID)
}

func TestListUsesConstantQueryCount(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	for i := 0; i < 20; i++ {
		root := ctx.create(t, i%len(ctx.users), fmt.Sprintf("root-%02d", i), nil)
		for child := 0; child < 3; child++ {
			ctx.create(t, (i+child+1)%len(ctx.users), fmt.Sprintf("child-%02d-%d", i, child), &root.ID)
		}
	}
	var queries atomic.Int64
	callbackName := "test:count_comment_list_queries"
	require.NoError(t, ctx.db.Callback().Query().Before("gorm:query").Register(callbackName, func(*gorm.DB) {
		queries.Add(1)
	}))
	t.Cleanup(func() { _ = ctx.db.Callback().Query().Remove(callbackName) })

	listed, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, Sort: SortOldest})
	require.NoError(t, err)
	require.Len(t, listed.Items, 20)
	for _, root := range listed.Items {
		require.Len(t, root.Replies, 3)
	}
	require.LessOrEqual(t, queries.Load(), int64(10))
}

func TestCommentDTODoesNotExposeContentHash(t *testing.T) {
	payload, err := json.Marshal(CommentDTO{})
	require.NoError(t, err)
	require.NotContains(t, string(payload), "content_hash")
}

func TestListReturnsDatabaseErrors(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	sqlDB, err := ctx.db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	_, err = ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1})
	require.Error(t, err)
}

func TestCreateRechecksLockedForumTargetInsideTransaction(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.MediaAsset{}, &model.ForumCategory{}, &model.ForumTopic{}, &model.DiscussionTarget{}, &model.CommentEntry{}, &model.CommentMention{}, &model.CommentAttachment{}, &model.CommentLike{}, &model.CommentReport{}, &model.CommentTimeAnchor{}, &model.CommentPublishRecord{}, &model.Notification{}, &model.AuditLog{})
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_discussion_target_kind_key ON discussion_targets (kind, resource_key)`).Error)
	user := model.User{Username: "locked-author", Email: "locked-author@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	category := model.ForumCategory{Name: "Locked", Color: "#111111"}
	require.NoError(t, db.Create(&category).Error)
	topic := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "Locked", Content: "Body", Closed: true}
	require.NoError(t, db.Create(&topic).Error)
	registry := &TargetRegistry{resolvers: map[string]TargetResolver{TargetKindForumTopic: targetResolverFunc(func(_ Viewer, id uuid.UUID) (ResolvedTarget, error) {
		return ResolvedTarget{Kind: TargetKindForumTopic, ResourceID: id, ResourceKey: id.String(), OwnerID: &user.UUID, Visible: true}, nil
	})}}
	service := NewService(db, registry)
	_, err := service.Create(authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}, TargetRef{Kind: TargetKindForumTopic, ResourceID: topic.ID}, CreateCommentInput{Content: "must not publish"})
	require.ErrorIs(t, err, ErrTargetLocked)
	var count int64
	require.NoError(t, db.Model(&model.CommentEntry{}).Count(&count).Error)
	require.Zero(t, count)
}
