package forum

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func createForumSearchComment(t *testing.T, db *gorm.DB, topicID, authorID uuid.UUID, content string) model.CommentEntry {
	t.Helper()
	var target model.DiscussionTarget
	if err := db.Where("kind = ? AND resource_id = ?", "forum_topic", topicID).First(&target).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("find discussion target: %v", err)
		}
		target = model.DiscussionTarget{Kind: "forum_topic", ResourceID: topicID, ResourceKey: topicID.String()}
		if err := db.Create(&target).Error; err != nil {
			t.Fatalf("create discussion target: %v", err)
		}
	}
	entry := model.CommentEntry{TargetID: target.ID, AuthorID: authorID, Content: content, ContentHash: uuid.NewString(), Status: "active"}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create forum comment: %v", err)
	}
	return entry
}

func TestSearchTopicsCoversForumContentWithoutDuplicates(t *testing.T) {
	router, db, owner, category := newForumHTTPTestRouter(t)
	replyAuthor := model.User{Username: "reply-orchid-author", Email: "reply-orchid@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&replyAuthor).Error; err != nil {
		t.Fatalf("create reply author: %v", err)
	}
	orchidCategory := model.ForumCategory{Name: "Orchid category", Description: "category match"}
	if err := db.Create(&orchidCategory).Error; err != nil {
		t.Fatalf("create matching category: %v", err)
	}

	topics := []model.ForumTopic{
		{UserID: owner.UUID, CategoryID: category.ID, Title: "Orchid title", Content: "plain"},
		{UserID: owner.UUID, CategoryID: category.ID, Title: "Body match", Content: "orchid in body"},
		{UserID: owner.UUID, CategoryID: category.ID, Title: "Reply match", Content: "plain"},
		{UserID: replyAuthor.UUID, CategoryID: category.ID, Title: "Author match", Content: "plain"},
		{UserID: owner.UUID, CategoryID: orchidCategory.ID, Title: "Category match", Content: "plain"},
		{UserID: owner.UUID, CategoryID: category.ID, Title: "Tag match", Content: "plain", Tags: model.StringSlice{"orchid"}},
	}
	for index := range topics {
		if err := db.Create(&topics[index]).Error; err != nil {
			t.Fatalf("create topic %d: %v", index, err)
		}
	}
	for floor := 1; floor <= 2; floor++ {
		createForumSearchComment(t, db, topics[2].ID, replyAuthor.UUID, "orchid reply")
	}

	response := performForumRequest(t, router, http.MethodGet, "/api/v1/forum/search?q=ORCHID&page=1&page_size=2", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	firstPage, envelope := decodeForumData[[]model.ForumTopic](t, response)
	if envelope.Meta.Total != 6 || len(firstPage) != 2 {
		t.Fatalf("expected six unique matches and two first-page rows, got data=%#v meta=%#v", firstPage, envelope.Meta)
	}

	seen := make(map[string]bool, 6)
	for page := 1; page <= 3; page++ {
		response = performForumRequest(t, router, http.MethodGet, "/api/v1/forum/search?q=orchid&page="+string(rune('0'+page))+"&page_size=2", nil)
		listed, pageEnvelope := decodeForumData[[]model.ForumTopic](t, response)
		if pageEnvelope.Meta.Total != 6 {
			t.Fatalf("page %d expected total 6, got %d", page, pageEnvelope.Meta.Total)
		}
		for _, topic := range listed {
			if seen[topic.ID.String()] {
				t.Fatalf("topic %s appeared on multiple pages", topic.ID)
			}
			seen[topic.ID.String()] = true
		}
	}
	if len(seen) != 6 {
		t.Fatalf("expected six distinct paged results, got %d", len(seen))
	}
}

func TestSearchTopicsHonorsVisibilityAndSoftDeletes(t *testing.T) {
	router, db, owner, visibleCategory := newForumHTTPTestRouter(t)
	hiddenCategory := model.ForumCategory{Name: "Hidden", Description: "restricted"}
	if err := db.Create(&hiddenCategory).Error; err != nil {
		t.Fatalf("create hidden category: %v", err)
	}
	group := model.ForumGroup{Name: "private-search-group"}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	permission := model.ForumCategoryPermission{CategoryID: hiddenCategory.ID, GroupID: group.ID, CanView: true}
	if err := db.Create(&permission).Error; err != nil {
		t.Fatalf("create permission: %v", err)
	}

	visible := model.ForumTopic{UserID: owner.UUID, CategoryID: visibleCategory.ID, Title: "Visible saffron", Content: "plain"}
	hidden := model.ForumTopic{UserID: owner.UUID, CategoryID: hiddenCategory.ID, Title: "Hidden saffron", Content: "plain"}
	deletedTopic := model.ForumTopic{UserID: owner.UUID, CategoryID: visibleCategory.ID, Title: "Deleted saffron", Content: "plain"}
	deletedReplyTopic := model.ForumTopic{UserID: owner.UUID, CategoryID: visibleCategory.ID, Title: "Deleted reply host", Content: "plain"}
	for _, topic := range []*model.ForumTopic{&visible, &hidden, &deletedTopic, &deletedReplyTopic} {
		if err := db.Create(topic).Error; err != nil {
			t.Fatalf("create topic: %v", err)
		}
	}
	deletedReply := createForumSearchComment(t, db, deletedReplyTopic.ID, owner.UUID, "saffron reply")
	if err := db.Delete(&deletedTopic).Error; err != nil {
		t.Fatalf("delete topic: %v", err)
	}
	if err := db.Delete(&deletedReply).Error; err != nil {
		t.Fatalf("delete reply: %v", err)
	}

	response := performForumRequest(t, router, http.MethodGet, "/api/v1/forum/search?q=saffron", nil)
	listed, envelope := decodeForumData[[]model.ForumTopic](t, response)
	if envelope.Meta.Total != 1 || len(listed) != 1 || listed[0].ID != visible.ID {
		t.Fatalf("expected only visible live topic, got data=%#v meta=%#v", listed, envelope.Meta)
	}
}

func TestSearchTopicsMatchesTopicAndReplyAuthorDisplayNames(t *testing.T) {
	router, db, owner, category := newForumHTTPTestRouter(t)
	topicAuthor := model.User{
		Username: "topic-author-user", DisplayName: "Cobalt Topic Author",
		Email: "topic-display-name@example.com", Password: "hash", IsActive: true,
	}
	replyAuthor := model.User{
		Username: "reply-author-user", DisplayName: "Cobalt Reply Author",
		Email: "reply-display-name@example.com", Password: "hash", IsActive: true,
	}
	for _, user := range []*model.User{&topicAuthor, &replyAuthor} {
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("create display-name author: %v", err)
		}
	}

	topicAuthorMatch := model.ForumTopic{UserID: topicAuthor.UUID, CategoryID: category.ID, Title: "Topic author match", Content: "plain"}
	replyAuthorMatch := model.ForumTopic{UserID: owner.UUID, CategoryID: category.ID, Title: "Reply author match", Content: "plain"}
	for _, topic := range []*model.ForumTopic{&topicAuthorMatch, &replyAuthorMatch} {
		if err := db.Create(topic).Error; err != nil {
			t.Fatalf("create topic: %v", err)
		}
	}
	createForumSearchComment(t, db, replyAuthorMatch.ID, replyAuthor.UUID, "plain")

	response := performForumRequest(t, router, http.MethodGet, "/api/v1/forum/search?q=cobalt", nil)
	listed, envelope := decodeForumData[[]model.ForumTopic](t, response)
	if envelope.Meta.Total != 2 || len(listed) != 2 {
		t.Fatalf("expected topic and reply author display-name matches, got data=%#v meta=%#v", listed, envelope.Meta)
	}
}

func TestSearchTopicsEmptyQueryDiffersFromTopicList(t *testing.T) {
	router, db, owner, category := newForumHTTPTestRouter(t)
	topic := model.ForumTopic{UserID: owner.UUID, CategoryID: category.ID, Title: "Existing", Content: "Body"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}

	response := performForumRequest(t, router, http.MethodGet, "/api/v1/forum/search?q="+url.QueryEscape("   "), nil)
	searched, searchEnvelope := decodeForumData[[]model.ForumTopic](t, response)
	if searchEnvelope.Meta.Total != 0 || len(searched) != 0 {
		t.Fatalf("expected empty search result, got data=%#v meta=%#v", searched, searchEnvelope.Meta)
	}

	response = performForumRequest(t, router, http.MethodGet, "/api/v1/forum/topics?search="+url.QueryEscape("   "), nil)
	listed, listEnvelope := decodeForumData[[]model.ForumTopic](t, response)
	if listEnvelope.Meta.Total != 1 || len(listed) != 1 {
		t.Fatalf("expected blank topic filter to preserve list behavior, got data=%#v meta=%#v", listed, listEnvelope.Meta)
	}
}

func TestPostgresSearchUsesParameterizedWeightedFullTextQuery(t *testing.T) {
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=localhost user=atoman dbname=atoman sslmode=disable",
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}

	search := "orchid & unsafe"
	query := NewRepo(db).visibleCategories(db.Model(&model.ForumTopic{}), authctx.CurrentUser{Role: authctx.RoleAdmin}, "forum_topics.category_id")
	statement := applyPostgresTopicSearch(query, search).Find(&[]model.ForumTopic{}).Statement
	sql := statement.SQL.String()

	for _, fragment := range []string{
		"websearch_to_tsquery",
		"to_tsvector",
		"setweight",
		"ILIKE",
		"discussion_targets",
		"comment_entries",
		"ce.deleted_at IS NULL",
		"topic_user.display_name",
		"comment_user.display_name",
		"MAX(",
		"search_rank DESC",
		"forum_topics.id DESC",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("expected PostgreSQL search SQL to contain %q, got: %s", fragment, sql)
		}
	}
	if strings.Contains(sql, search) {
		t.Fatalf("expected search input to be parameterized, got SQL: %s", sql)
	}
	if strings.Contains(strings.ToLower(sql), "string_agg") {
		t.Fatalf("expected per-reply ranking without unbounded aggregation, got SQL: %s", sql)
	}
	boundSearch := false
	boundHybridPattern := false
	for _, variable := range statement.Vars {
		if variable == search {
			boundSearch = true
		}
		if variable == "%"+search+"%" {
			boundHybridPattern = true
		}
	}
	if !boundSearch || !boundHybridPattern {
		t.Fatalf("expected raw search input in bound variables, got %#v", statement.Vars)
	}
}

func TestPostgresSearchMatchesContinuousChineseText(t *testing.T) {
	dsn := os.Getenv("FORUM_TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = "postgres://atoman:atoman_secret@localhost:5432/postgres?sslmode=disable"
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := sqlDB.Ping(); err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}

	schema := "forum_search_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS ltree`).Error; err != nil {
		t.Fatalf("enable ltree: %v", err)
	}
	if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm`).Error; err != nil {
		t.Fatalf("enable pg_trgm: %v", err)
	}
	if err := db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
	})
	if err := db.Exec("SET search_path TO " + schema + ", public").Error; err != nil {
		t.Fatalf("set search path: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.ForumCategory{}, &model.ForumTopic{}, &model.DiscussionTarget{}, &model.CommentEntry{}, &model.ForumUserTrust{}); err != nil {
		t.Fatalf("migrate PostgreSQL test schema: %v", err)
	}

	owner := model.User{Username: "chinese-search-owner", Email: fmt.Sprintf("%s@example.com", schema), Password: "hash", Role: authctx.RoleAdmin, IsActive: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	category := model.ForumCategory{Name: "中文搜索分类"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	titleMatch := model.ForumTopic{UserID: owner.UUID, CategoryID: category.ID, Title: "包含中文短词的标题", Content: "plain"}
	replyMatch := model.ForumTopic{UserID: owner.UUID, CategoryID: category.ID, Title: "reply host", Content: "plain"}
	for _, topic := range []*model.ForumTopic{&titleMatch, &replyMatch} {
		if err := db.Create(topic).Error; err != nil {
			t.Fatalf("create topic: %v", err)
		}
	}
	createForumSearchComment(t, db, replyMatch.ID, owner.UUID, "回复也包含中文短词")

	topics, total, err := NewRepo(db).SearchTopics(
		authctx.CurrentUser{ID: owner.UUID, Username: owner.Username, Role: authctx.RoleAdmin},
		ListTopicsQuery{Search: "中文短词", Page: 1, PageSize: 20},
	)
	if err != nil {
		t.Fatalf("search Chinese text: %v", err)
	}
	if total != 2 || len(topics) != 2 {
		t.Fatalf("expected title and reply Chinese matches, got total=%d topics=%#v", total, topics)
	}
}
