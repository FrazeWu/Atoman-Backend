package books

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAdvancedWorkSearchFiltersRankingAndDeduplication(t *testing.T) {
	s, user := newBookWikiTestService(t)
	ctx := context.Background()
	author, err := s.CreateBookPerson(ctx, user, CreateBookPersonInput{Name: "星际作者"})
	require.NoError(t, err)
	work, err := s.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "星际", AuthorIDs: []uuid.UUID{author.ID}, Authors: []string{"共同作者"}})
	require.NoError(t, err)
	id := uuid.MustParse(work.ID)
	require.NoError(t, s.db.Model(&model.BookWork{}).Where("id = ?", id).Updates(map[string]any{"language": "zh", "original_title": "Original Star", "subtitle": "宇宙副标题"}).Error)
	for _, date := range []string{"1999-12-31", "2000-01-01", "2026-12-31"} {
		_, err := s.CreateBookEdition(ctx, user, id, CreateBookEditionInput{PublishedDate: date, Language: "en"})
		require.NoError(t, err)
	}
	_, err = s.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "星际续集", AuthorIDs: []uuid.UUID{author.ID}})
	require.NoError(t, err)
	for _, query := range []string{"星际", "Original Star", "宇宙副标题", "共同作者"} {
		result, err := s.SearchWorksAdvanced(ctx, WorkSearchOptions{BookSearchOptions: BookSearchOptions{Query: query, Sort: "relevance", ExactTotal: true, Limit: 1}})
		require.NoError(t, err)
		require.Equal(t, id, result.Items[0].ID, query)
		require.Len(t, result.Items[0].Authors, 2)
	}
	for _, tc := range []struct {
		name string
		opts WorkSearchOptions
		want int64
	}{
		{"同字段 OR、跨字段 AND", WorkSearchOptions{Languages: []string{"zh", "fr"}, AuthorIDs: []uuid.UUID{author.ID, uuid.New()}, YearFrom: 2000, YearTo: 2026}, 1},
		{"不使用版本语言", WorkSearchOptions{Languages: []string{"en"}}, 0},
		{"年份上下界必须同一版本", WorkSearchOptions{YearFrom: 2001, YearTo: 2025}, 0},
		{"年份含首尾", WorkSearchOptions{YearFrom: 2026, YearTo: 2026}, 1},
		{"作者不匹配", WorkSearchOptions{AuthorIDs: []uuid.UUID{uuid.New()}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.BookSearchOptions = BookSearchOptions{ExactTotal: true, Limit: 50}
			result, err := s.SearchWorksAdvanced(ctx, tc.opts)
			require.NoError(t, err)
			require.Equal(t, tc.want, *result.Total)
			require.Len(t, result.Items, int(tc.want))
		})
	}
	// 多版本、多作者不会重复；分页在数据库中完成。
	first, err := s.SearchWorksAdvanced(ctx, WorkSearchOptions{BookSearchOptions: BookSearchOptions{Query: "星际", Sort: "relevance", ExactTotal: true, Limit: 1}})
	require.NoError(t, err)
	second, err := s.SearchWorksAdvanced(ctx, WorkSearchOptions{BookSearchOptions: BookSearchOptions{Query: "星际", Sort: "relevance", ExactTotal: true, Limit: 1, Offset: 1}})
	require.NoError(t, err)
	require.EqualValues(t, 2, *first.Total)
	require.NotEqual(t, first.Items[0].ID, second.Items[0].ID)
}

func TestAdvancedBookSearchVisibilityAndWildcardLiterals(t *testing.T) {
	s, user := newBookWikiTestService(t)
	ctx := context.Background()
	for _, title := range []string{`100%_\`, "100abcd", "草稿", "关闭", "合并", "删除"} {
		work, err := s.CreateBookWork(ctx, user, CreateBookWorkInput{Title: title, Authors: []string{title}})
		require.NoError(t, err)
		switch title {
		case "草稿":
			require.NoError(t, s.db.Model(&model.BookWork{}).Where("id = ?", work.ID).Update("lifecycle_status", "draft").Error)
		case "关闭":
			require.NoError(t, s.db.Model(&model.BookWork{}).Where("id = ?", work.ID).Update("edit_status", "closed").Error)
		case "合并":
			require.NoError(t, s.db.Model(&model.BookWork{}).Where("id = ?", work.ID).Update("lifecycle_status", "merged").Error)
		case "删除":
			require.NoError(t, s.db.Delete(&model.BookWork{}, "id = ?", work.ID).Error)
		}
	}
	require.NoError(t, s.db.Create(&model.BookWork{Title: "没有 Wiki 修订", LifecycleStatus: "active"}).Error)
	result, err := s.SearchWorksAdvanced(ctx, WorkSearchOptions{BookSearchOptions: BookSearchOptions{ExactTotal: true}})
	require.NoError(t, err)
	require.EqualValues(t, 2, *result.Total)
	for _, q := range []string{"%", "_", `\`, `100%_\`} {
		result, err := s.SearchWorksAdvanced(ctx, WorkSearchOptions{BookSearchOptions: BookSearchOptions{Query: q, ExactTotal: true}})
		require.NoError(t, err)
		require.EqualValues(t, 1, *result.Total, q)
	}
	// 草稿、停用及已合并作者不能匹配作品，也不能成为作者结果。
	for _, status := range []string{"draft", "retired", "merged"} {
		person := model.BookPerson{Name: "隐藏作者", LifecycleStatus: status}
		require.NoError(t, s.db.Create(&person).Error)
		require.NoError(t, s.db.Create(&model.BookContribution{WorkID: &result.Items[0].ID, PersonID: person.ID, Role: "author"}).Error)
	}
	result, err = s.SearchWorksAdvanced(ctx, WorkSearchOptions{BookSearchOptions: BookSearchOptions{Query: "隐藏作者", ExactTotal: true}})
	require.NoError(t, err)
	require.Zero(t, *result.Total)
	authors, err := s.SearchAuthorsAdvanced(ctx, BookSearchOptions{Query: "隐藏作者", ExactTotal: true})
	require.NoError(t, err)
	require.Zero(t, *authors.Total)
}

func TestAdvancedWorkSearchEditionVisibility(t *testing.T) {
	s, user := newBookWikiTestService(t)
	ctx := context.Background()
	work := newBookRatingTestWork(t, s, user, "版本边界")
	date := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, state := range []struct {
		lifecycle, edit string
		deleted         bool
	}{
		{"draft", "development", false}, {"merged", "development", false}, {"retired", "development", false},
		{"active", "closed", false}, {"active", "development", true},
	} {
		edition := model.BookEdition{WorkID: work, PublishedDate: &date, LifecycleStatus: state.lifecycle, EditStatus: state.edit, CoverURL: "/private-cover"}
		require.NoError(t, s.db.Create(&edition).Error)
		if state.deleted {
			require.NoError(t, s.db.Delete(&edition).Error)
		}
	}
	result, err := s.SearchWorksAdvanced(ctx, WorkSearchOptions{BookSearchOptions: BookSearchOptions{ExactTotal: true}, YearFrom: 2026, YearTo: 2026})
	require.NoError(t, err)
	require.Zero(t, *result.Total)
	result, err = s.SearchWorksAdvanced(ctx, WorkSearchOptions{BookSearchOptions: BookSearchOptions{ExactTotal: true}})
	require.NoError(t, err)
	require.Empty(t, result.Items[0].CoverURL)
}

func TestAdvancedBookSearchAuthorsAndPublishedLists(t *testing.T) {
	s, user := newBookWikiTestService(t)
	testdb.Migrate(t, s.db, &model.BookList{})
	ctx := context.Background()
	_, err := s.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "公开作品", Authors: []string{"作者"}})
	require.NoError(t, err)
	authors, err := s.SearchAuthorsAdvanced(ctx, BookSearchOptions{})
	require.NoError(t, err)
	work, err := s.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "关闭作品", AuthorIDs: []uuid.UUID{authors.Items[0].ID}})
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&model.BookWork{}).Where("id = ?", work.ID).Update("edit_status", "closed").Error)
	authors, err = s.SearchAuthorsAdvanced(ctx, BookSearchOptions{Query: "作者", Sort: "relevance", ExactTotal: true})
	require.NoError(t, err)
	require.Len(t, authors.Items, 1)
	require.EqualValues(t, 1, authors.Items[0].WorkCount)
	now := time.Now().UTC()
	for i, visibility := range []string{"public", "private", "public", "public"} {
		list := model.BookList{UserID: user.ID, Version: 2, DraftJSON: `{"title":"私人秘密"}`, PublishedJSON: `{"title":"阅读","description":"已发布简介","items":[]}`, PublishedVersion: 1, PublishedAt: &now, Visibility: visibility}
		if i == 2 {
			list.PublishedAt = nil
		}
		require.NoError(t, s.db.Create(&list).Error)
		if i == 3 {
			require.NoError(t, s.db.Delete(&list).Error)
		}
	}
	for _, q := range []string{"阅读", "已发布简介", "私人秘密"} {
		lists, err := s.SearchPublicBookListsAdvanced(ctx, BookSearchOptions{Query: q, Sort: "relevance", ExactTotal: true})
		require.NoError(t, err)
		if q == "私人秘密" {
			require.Zero(t, *lists.Total)
		} else {
			require.EqualValues(t, 1, *lists.Total)
			require.Equal(t, user.ID, lists.Items[0].OwnerID)
			require.Equal(t, "editor", lists.Items[0].OwnerName)
		}
	}
}

func TestAdvancedWorkSearchRatingUsesExistingGateAndWeights(t *testing.T) {
	s, user := newBookWikiTestService(t)
	ctx := context.Background()
	low := newBookRatingTestWork(t, s, user, "低分")
	high := newBookRatingTestWork(t, s, user, "高分")
	insufficient := newBookRatingTestWork(t, s, user, "样本不足")
	readers := make([]uuid.UUID, 0, 6)
	for i := range 6 {
		reader := model.User{UUID: uuid.New(), Username: fmt.Sprint("r", i), Email: fmt.Sprintf("r%d@example.test", i), Password: "hash", IsActive: true}
		require.NoError(t, s.db.Create(&reader).Error)
		readers = append(readers, reader.UUID)
		for _, vote := range []model.BookRating{{UserID: reader.UUID, WorkID: low, Score: 5}, {UserID: reader.UUID, WorkID: high, Score: 8}} {
			require.NoError(t, s.db.Create(&vote).Error)
		}
		if i < 5 {
			require.NoError(t, s.db.Create(&model.BookRating{UserID: reader.UUID, WorkID: insufficient, Score: 10}).Error)
		}
	}
	result, err := s.SearchWorksAdvanced(ctx, WorkSearchOptions{BookSearchOptions: BookSearchOptions{Sort: "rating_desc", ExactTotal: true, Limit: 1}})
	require.NoError(t, err)
	require.EqualValues(t, 2, *result.Total)
	require.Equal(t, high, result.Items[0].ID)
	require.True(t, result.Items[0].RatingActive)
	require.Equal(t, 8.0, *result.Items[0].WeightedRatingScore)
	result, err = s.SearchWorksAdvanced(ctx, WorkSearchOptions{BookSearchOptions: BookSearchOptions{Sort: "rating_desc", ExactTotal: true, Limit: 1, Offset: 1}})
	require.NoError(t, err)
	require.Equal(t, low, result.Items[0].ID)
	// 加权高分为 6，算术平均为 3.3；必须排在 5 分作品之前。
	testdb.Migrate(t, s.db, &model.ReputationRun{}, &model.UserReputationSnapshot{})
	now := time.Now().UTC()
	run := model.ReputationRun{Status: "published", StartedAt: now, PublishedAt: &now}
	require.NoError(t, s.db.Create(&run).Error)
	for i, readerID := range readers {
		weight, score := 0.2, 2
		if i == 0 {
			weight, score = 1, 10
		}
		require.NoError(t, s.db.Create(&model.UserReputationSnapshot{RunID: run.ID, UserID: readerID, EvaluationWeight: weight}).Error)
		require.NoError(t, s.db.Model(&model.BookRating{}).Where("work_id = ? AND user_id = ?", high, readerID).Update("score", score).Error)
	}
	result, err = s.SearchWorksAdvanced(ctx, WorkSearchOptions{BookSearchOptions: BookSearchOptions{Sort: "rating_desc", ExactTotal: true, Limit: 1}})
	require.NoError(t, err)
	require.Equal(t, high, result.Items[0].ID)
	require.Equal(t, 6.0, *result.Items[0].WeightedRatingScore)
	require.Equal(t, 3.3, result.Items[0].RatingScore)
	require.NoError(t, s.db.Model(&model.User{}).Where("uuid = ?", readers[0]).Update("is_active", false).Error)
	result, err = s.SearchWorksAdvanced(ctx, WorkSearchOptions{BookSearchOptions: BookSearchOptions{Sort: "rating_desc", ExactTotal: true}})
	require.NoError(t, err)
	require.Zero(t, *result.Total, "停用用户的评分不参与样本门槛")
}

func TestAdvancedBookSearchCountsOnlyWhenRequestedAndCancels(t *testing.T) {
	s, user := newBookWikiTestService(t)
	testdb.Migrate(t, s.db, &model.BookList{})
	newBookRatingTestWork(t, s, user, "查询计数")
	counts := 0
	require.NoError(t, s.db.Callback().Query().After("gorm:query").Register("test:search_counts", func(tx *gorm.DB) {
		if !tx.DryRun && strings.HasPrefix(strings.ToLower(tx.Statement.SQL.String()), "select count(*)") {
			counts++
		}
	}))
	for _, exact := range []bool{false, true} {
		counts = 0
		works, err := s.SearchWorksAdvanced(context.Background(), WorkSearchOptions{BookSearchOptions: BookSearchOptions{ExactTotal: exact}})
		require.NoError(t, err)
		authors, err := s.SearchAuthorsAdvanced(context.Background(), BookSearchOptions{ExactTotal: exact})
		require.NoError(t, err)
		lists, err := s.SearchPublicBookListsAdvanced(context.Background(), BookSearchOptions{ExactTotal: exact})
		require.NoError(t, err)
		if exact {
			require.Equal(t, 3, counts)
			require.NotNil(t, works.Total)
			require.NotNil(t, authors.Total)
			require.NotNil(t, lists.Total)
		} else {
			require.Zero(t, counts)
			require.Nil(t, works.Total)
			require.Nil(t, authors.Total)
			require.Nil(t, lists.Total)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.SearchWorksAdvanced(ctx, WorkSearchOptions{})
	require.ErrorIs(t, err, context.Canceled)
}
