package books

import (
	"context"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBookPostLinksExposeOnlyPublishedPostsOwnedByAuthor(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookSource{}, &model.BookRating{}, &model.BookPostLink{}, &model.Post{})
	service := NewService(db)
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	user := model.User{UUID: owner.ID, Username: "book-post-owner", Email: "book-post-owner@example.test", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	work := model.BookWork{Title: "Work", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&work).Error)
	published := model.Post{UserID: owner.ID, Title: "Long review", Summary: "Summary", Status: "published", Visibility: "public"}
	draft := model.Post{UserID: owner.ID, Title: "Draft review", Status: "draft", Visibility: "public"}
	require.NoError(t, db.Create(&published).Error)
	require.NoError(t, db.Create(&draft).Error)
	require.NoError(t, service.LinkBookPost(owner, work.ID, published.ID))

	posts, total, err := service.ListRelatedBookPosts(context.Background(), work.ID, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, posts, 1)
	require.Equal(t, "Long review", posts[0].Title)
	require.Error(t, service.LinkBookPost(owner, work.ID, draft.ID))
	require.NoError(t, service.UnlinkBookPost(owner, work.ID, published.ID))
	posts, total, err = service.ListRelatedBookPosts(context.Background(), work.ID, 20, 0)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, posts)
	require.NoError(t, service.LinkBookPost(owner, work.ID, published.ID))
	posts, total, err = service.ListRelatedBookPosts(context.Background(), work.ID, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, posts, 1)
}
