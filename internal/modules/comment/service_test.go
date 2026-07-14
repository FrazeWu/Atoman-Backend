package comment

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

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
			return ResolvedTarget{Kind: TargetKindBlogPost, ResourceKey: otherID.String(), Visible: true}, nil
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
	require.NotEmpty(t, one.ContentHash)
	require.Equal(t, one.ContentHash, two.ContentHash)
	require.NotEqual(t, one.ContentHash, three.ContentHash)
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

	latest, err := ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, Sort: SortLatest})
	require.NoError(t, err)
	require.Equal(t, 22, *latest.Items[0].FloorNumber)
	_, err = ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 0, Sort: SortOldest})
	require.ErrorIs(t, err, ErrInvalidListOptions)
	_, err = ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1, Sort: "random"})
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

func TestListMarkedRootDoesNotConsumeOrdinaryPageSize(t *testing.T) {
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
	require.Len(t, listed.Items, 21)
	require.Equal(t, marked.ID, listed.Items[0].ID)
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

func TestListReturnsDatabaseErrors(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	sqlDB, err := ctx.db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	_, err = ctx.service.List(ctx.users[0], ctx.target, ListCommentsInput{Page: 1})
	require.Error(t, err)
}
