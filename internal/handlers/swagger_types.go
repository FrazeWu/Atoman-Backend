package handlers

import "atoman/internal/model"

type ErrorResponse struct {
	Error   string `json:"error" example:"用户名和密码不匹配"`
	Details string `json:"details,omitempty" example:"mail provider unavailable"`
}

type MessageResponse struct {
	Message string `json:"message" example:"Verification code sent"`
}

type AuthUserResponse struct {
	UUID                  string  `json:"uuid" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	ID                    uint    `json:"id" example:"1"`
	Username              string  `json:"username" example:"fafa"`
	Email                 string  `json:"email" example:"fafa@example.com"`
	Role                  string  `json:"role" example:"user"`
	DisplayName           string  `json:"display_name" example:"Fafa"`
	AvatarURL             string  `json:"avatar_url" example:"https://cdn.example.com/avatar.png"`
	IsActive              bool    `json:"is_active" example:"true"`
	OnboardingCompletedAt *string `json:"onboarding_completed_at" format:"date-time" example:"2026-06-02T09:30:00Z"`
}

type OnboardingCompleteResponse struct {
	OnboardingCompletedAt string `json:"onboarding_completed_at" format:"date-time" example:"2026-06-02T09:30:00Z"`
}

type AuthSuccessResponse struct {
	Token string           `json:"token" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"`
	User  AuthUserResponse `json:"user"`
}

type UserResponse struct {
	Data    model.User `json:"data"`
	Message string     `json:"message" example:"ok"`
}

type UserListResponse struct {
	Data    []model.User `json:"data"`
	Message string       `json:"message" example:"ok"`
}

type UserSettingsResponse struct {
	Data    model.UserSettings `json:"data"`
	Message string             `json:"message" example:"ok"`
}

type ExplorePostListResponse struct {
	Data    []ExplorePostResponse `json:"data"`
	Message string                `json:"message" example:"ok"`
}

type SearchUserSummary struct {
	UUID        string `json:"uuid" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Username    string `json:"username" example:"fafa"`
	DisplayName string `json:"display_name" example:"Fafa"`
	AvatarURL   string `json:"avatar_url" example:"https://cdn.example.com/avatar.png"`
	Role        string `json:"role" example:"user"`
}

type SearchUsersResponse struct {
	Data []SearchUserSummary `json:"data"`
}

type UserRoleSummary struct {
	UUID        string `json:"uuid" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Username    string `json:"username" example:"fafa"`
	Email       string `json:"email" example:"fafa@example.com"`
	DisplayName string `json:"display_name" example:"Fafa"`
	AvatarURL   string `json:"avatar_url" example:"https://cdn.example.com/avatar.png"`
	Role        string `json:"role" example:"user"`
	CreatedAt   string `json:"created_at" format:"date-time" example:"2026-05-25T11:00:00Z"`
}

type UserRoleListResponse struct {
	Data []UserRoleSummary `json:"data"`
}

type UpdateUserRoleInput struct {
	Role string `json:"role" example:"admin"`
}

type UserRoleResponse struct {
	Data UserRoleSummary `json:"data"`
}

type UserLookupData struct {
	ID             uint   `json:"id" example:"1"`
	UUID           string `json:"uuid" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Username       string `json:"username" example:"fafa"`
	DisplayName    string `json:"display_name" example:"Fafa"`
	AvatarURL      string `json:"avatar_url" example:"https://cdn.example.com/avatar.png"`
	Bio            string `json:"bio" example:"Writer and builder"`
	Website        string `json:"website" example:"https://atoman.org"`
	Role           string `json:"role" example:"user"`
	CreatedAt      string `json:"created_at" format:"date-time" example:"2026-05-25T11:00:00Z"`
	FollowersCount int64  `json:"followers_count" example:"12"`
	FollowingCount int64  `json:"following_count" example:"7"`
	PostsCount     int64  `json:"posts_count" example:"5"`
}

type UserLookupResponse struct {
	Data    UserLookupData `json:"data"`
	Message string         `json:"message" example:"ok"`
}

type PublicProfileUser struct {
	ID          uint   `json:"id" example:"1"`
	UUID        string `json:"uuid" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Username    string `json:"username" example:"fafa"`
	DisplayName string `json:"display_name" example:"Fafa"`
	AvatarURL   string `json:"avatar_url" example:"https://cdn.example.com/avatar.png"`
	Bio         string `json:"bio" example:"Writer and builder"`
	Website     string `json:"website" example:"https://atoman.org"`
	Location    string `json:"location" example:"Seoul"`
	CreatedAt   string `json:"created_at" format:"date-time" example:"2026-05-25T11:00:00Z"`
}

type UserProfileStats struct {
	FollowersCount int64 `json:"followers_count" example:"12"`
	FollowingCount int64 `json:"following_count" example:"7"`
	PostsCount     int64 `json:"posts_count" example:"5"`
}

type UserProfileData struct {
	User     PublicProfileUser `json:"user"`
	Stats    UserProfileStats  `json:"stats"`
	Channels []model.Channel   `json:"channels"`
}

type UserProfileResponse struct {
	Data    UserProfileData `json:"data"`
	Message string          `json:"message" example:"ok"`
}

type PostResponse struct {
	Data    model.Post `json:"data"`
	Message string     `json:"message" example:"ok"`
}

type PostListResponse struct {
	Data    []model.Post `json:"data"`
	Message string       `json:"message" example:"ok"`
}

type BlogDraftEnvelope struct {
	Data    BlogDraftResponse `json:"data"`
	Message string            `json:"message" example:"ok"`
}

type ChannelResponse struct {
	Data    model.Channel `json:"data"`
	Message string        `json:"message" example:"ok"`
}

type ChannelListResponse struct {
	Data    []model.Channel `json:"data"`
	Message string          `json:"message" example:"ok"`
}

type CollectionResponse struct {
	Data    model.Collection `json:"data"`
	Message string           `json:"message" example:"ok"`
}

type CollectionListResponse struct {
	Data    []model.Collection `json:"data"`
	Message string             `json:"message" example:"ok"`
}

type UserCollectionItem struct {
	ID          string `json:"id" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	CreatedAt   string `json:"created_at" format:"date-time" example:"2026-05-25T11:00:00Z"`
	UpdatedAt   string `json:"updated_at" format:"date-time" example:"2026-05-25T11:00:00Z"`
	ChannelID   string `json:"channel_id" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8b"`
	Name        string `json:"name" example:"Essays"`
	Description string `json:"description" example:"Long-form writing"`
	CoverURL    string `json:"cover_url" example:"https://cdn.example.com/cover.png"`
	IsDefault   bool   `json:"is_default" example:"false"`
	ChannelName string `json:"channel_name" example:"Fafa Channel"`
}

type UserCollectionListResponse struct {
	Data    []UserCollectionItem `json:"data"`
	Message string               `json:"message" example:"ok"`
}

type CommentResponse struct {
	Data    model.Comment `json:"data"`
	Message string        `json:"message" example:"ok"`
}

type CommentListResponse struct {
	Data    []model.Comment `json:"data"`
	Message string          `json:"message" example:"ok"`
}

type LikeCountData struct {
	Count int64 `json:"count" example:"12"`
}

type LikeCountResponse struct {
	Data    LikeCountData `json:"data"`
	Message string        `json:"message" example:"ok"`
}

type BookmarkResponse struct {
	Data    model.Bookmark `json:"data"`
	Message string         `json:"message" example:"ok"`
}

type BookmarkListResponse struct {
	Data    []model.Bookmark `json:"data"`
	Message string           `json:"message" example:"ok"`
}

type BookmarkFolderResponse struct {
	Data    model.BookmarkFolder `json:"data"`
	Message string               `json:"message" example:"ok"`
}

type BookmarkFolderListResponse struct {
	Data    []model.BookmarkFolder `json:"data"`
	Message string                 `json:"message" example:"ok"`
}

type DiscussionContentInput struct {
	Content string `json:"content" binding:"required" example:"这段内容需要补充来源。"`
}

type DiscussionResponse struct {
	Data model.Discussion `json:"data"`
}

type DiscussionListResponse struct {
	Data  []model.Discussion `json:"data"`
	Total int64              `json:"total" example:"12"`
}

type DiscussionUnreadCountData struct {
	UnreadCount int64 `json:"unread_count" example:"3"`
}

type DiscussionUnreadCountResponse struct {
	Data DiscussionUnreadCountData `json:"data"`
}

type ProtectionPayload struct {
	ProtectionLevel string      `json:"protection_level" example:"semi"`
	Reason          string      `json:"reason,omitempty" example:"条目进入审核期"`
	ProtectedBy     string      `json:"protected_by,omitempty" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	ProtectedUser   *model.User `json:"protected_user,omitempty"`
	ExpiresAt       string      `json:"expires_at,omitempty" format:"date-time" example:"2026-06-01T00:00:00Z"`
	CreatedAt       string      `json:"created_at,omitempty" format:"date-time" example:"2026-05-25T12:00:00Z"`
}

type ProtectionResponse struct {
	Data ProtectionPayload `json:"data"`
}

type ProtectionActionResponse struct {
	Message string                  `json:"message" example:"Protection set successfully"`
	Data    model.ContentProtection `json:"data"`
}

type ForumCategoryInput struct {
	Name        string `json:"name" binding:"required" example:"General"`
	Description string `json:"description" example:"General discussion"`
	Color       string `json:"color" example:"#000000"`
}

type ForumTopicCreateInput struct {
	CategoryID string   `json:"category_id" binding:"required" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Title      string   `json:"title" binding:"required" example:"How to organize music metadata?"`
	Content    string   `json:"content" binding:"required" example:"Let's discuss best practices."`
	Tags       []string `json:"tags" example:"workflow,metadata"`
}

type ForumTopicUpdateInput struct {
	Title   string   `json:"title" example:"Updated title"`
	Content string   `json:"content" example:"Updated content"`
	Tags    []string `json:"tags" example:"workflow,updated"`
}

type ForumReplyCreateInput struct {
	Content       string  `json:"content" binding:"required" example:"I think folder-based grouping works well."`
	ParentReplyID *string `json:"parent_reply_id,omitempty" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
}

type ForumReplyUpdateInput struct {
	Content string `json:"content" binding:"required" example:"Updated reply body"`
}

type ForumDraftInput struct {
	ContextKey string `json:"context_key" binding:"required" example:"new_topic"`
	Title      string `json:"title" example:"Draft title"`
	Content    string `json:"content" example:"Draft content"`
	Tags       string `json:"tags" example:"tag1,tag2"`
}

type ForumReportInput struct {
	TargetType string `json:"target_type" binding:"required" example:"topic"`
	TargetID   string `json:"target_id" binding:"required" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Reason     string `json:"reason" binding:"required" example:"spam"`
	Note       string `json:"note" example:"Repeated promotional links"`
}

type CategoryRequestCreateInput struct {
	Name        string `json:"name" binding:"required" example:"Production"`
	Description string `json:"description" example:"Discuss release engineering"`
	Reason      string `json:"reason" example:"There are enough related threads now"`
}

type CategoryRequestReviewInput struct {
	Action     string `json:"action" binding:"required" example:"approve"`
	ReviewNote string `json:"review_note" example:"Makes sense for current traffic"`
	Color      string `json:"color" example:"#6366f1"`
}

type ForumCategoryResponse struct {
	Data model.ForumCategory `json:"data"`
}

type ForumCategoryListResponse struct {
	Data []model.ForumCategory `json:"data"`
}

type ForumTopicResponse struct {
	Data model.ForumTopic `json:"data"`
}

type ForumTopicListResponse struct {
	Data  []model.ForumTopic `json:"data"`
	Total int64              `json:"total" example:"42"`
	Page  int                `json:"page" example:"1"`
	Limit int                `json:"limit" example:"20"`
}

type ForumReplyResponse struct {
	Data model.ForumReply `json:"data"`
}

type ForumReplyListResponse struct {
	Data []model.ForumReply `json:"data"`
}

type ForumDraftResponse struct {
	Data model.ForumDraft `json:"data"`
}

type ForumSearchResponse struct {
	Data  []model.ForumTopic `json:"data"`
	Total int64              `json:"total" example:"12"`
	Page  int                `json:"page" example:"1"`
	Limit int                `json:"limit" example:"20"`
	Q     string             `json:"q" example:"music"`
}

type BoolStatusResponse struct {
	Liked      bool `json:"liked,omitempty" example:"true"`
	Bookmarked bool `json:"bookmarked,omitempty" example:"true"`
	Pinned     bool `json:"pinned,omitempty" example:"true"`
	Closed     bool `json:"closed,omitempty" example:"false"`
	Ok         bool `json:"ok,omitempty" example:"true"`
}

type CategoryRequestResponse struct {
	Data model.CategoryRequest `json:"data"`
}

type CategoryRequestListResponse struct {
	Data []model.CategoryRequest `json:"data"`
}

type CategoryRequestReviewResponse struct {
	Data     model.CategoryRequest `json:"data"`
	Category model.ForumCategory   `json:"category"`
}

type NotificationListResponse struct {
	Data  []model.Notification `json:"data"`
	Total int64                `json:"total" example:"15"`
	Page  int                  `json:"page" example:"1"`
}

type CountResponse struct {
	Count int64 `json:"count" example:"8"`
}

type DMConversationItemResponse struct {
	ConversationID string `json:"conversation_id" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	OtherUsername  string `json:"other_username" example:"alice"`
	OtherUserID    string `json:"other_user_id" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8b"`
	LastMessageAt  string `json:"last_message_at,omitempty" format:"date-time" example:"2026-05-25T11:00:00Z"`
	Preview        string `json:"preview" example:"Hello there"`
	UnreadCount    int64  `json:"unread_count" example:"2"`
}

type DMConversationListResponse struct {
	Data []DMConversationItemResponse `json:"data"`
}

type DMMessageListResponse struct {
	Data  []model.DMMessage `json:"data"`
	Total int64             `json:"total" example:"30"`
	Page  int               `json:"page" example:"1"`
}

type DMMessageResponse struct {
	Data model.DMMessage `json:"data"`
}

type DMSendInput struct {
	Content  string `json:"content" example:"Hi there"`
	ImageURL string `json:"image_url" example:"https://cdn.example.com/dm.png"`
}

type ImageUploadResponse struct {
	ImageURL string `json:"image_url" example:"https://cdn.example.com/uploaded.png"`
}

type ConflictWithIDResponse struct {
	Error string `json:"error" example:"Album already exists"`
	ID    string `json:"id" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
}

type SongPublicItem struct {
	ID          string `json:"id" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Title       string `json:"title" example:"Track One"`
	Artist      string `json:"artist" example:"Fafa"`
	Album       string `json:"album" example:"Paper Notes"`
	AlbumID     string `json:"album_id,omitempty" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8b"`
	Year        int    `json:"year" example:"2026"`
	ReleaseDate string `json:"release_date" example:"2026-05-25"`
	Lyrics      string `json:"lyrics" example:"Hello world"`
	AudioURL    string `json:"audio_url" example:"https://cdn.example.com/song.mp3"`
	CoverURL    string `json:"cover_url" example:"https://cdn.example.com/cover.jpg"`
	Status      string `json:"status" example:"open"`
}

type ArtistUpdateInput struct {
	Name        string `json:"name" example:"Fafa"`
	Bio         string `json:"bio" example:"Independent artist and writer"`
	Nationality string `json:"nationality" example:"CN"`
	BirthYear   int    `json:"birth_year" example:"1995"`
	DeathYear   int    `json:"death_year" example:"0"`
	Members     string `json:"members" example:"Member A, Member B"`
	ImageURL    string `json:"image_url" example:"https://cdn.example.com/artist.jpg"`
	EditSummary string `json:"edit_summary" example:"补充艺人简介"`
}

type ArtistWikiResponse struct {
	Data       model.Artist `json:"data"`
	RedirectTo string       `json:"redirect_to,omitempty" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8b"`
}

type ArtistAliasInput struct {
	Alias      string `json:"alias" binding:"required" example:"FAFA"`
	IsMainName bool   `json:"is_main_name" example:"false"`
}

type ArtistAliasResponse struct {
	Data model.ArtistAlias `json:"data"`
}

type ArtistAliasListResponse struct {
	Data []model.ArtistAlias `json:"data"`
}

type ArtistMergeInput struct {
	SourceID string `json:"source_id" format:"uuid" binding:"required" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
}

type RevisionResponse struct {
	Data model.Revision `json:"data"`
}

type RevisionListResponse struct {
	Data   []model.Revision `json:"data"`
	Total  int64            `json:"total" example:"12"`
	Limit  int              `json:"limit" example:"50"`
	Offset int              `json:"offset" example:"0"`
}

type RevisionActionResponse struct {
	Data    model.Revision `json:"data"`
	Message string         `json:"message" example:"Changes saved and pending approval"`
}

type RevisionDiffResponse struct {
	Data map[string]interface{} `json:"data"`
}

type RevisionConflictResponse struct {
	Error     string               `json:"error" example:"Edit conflicts detected"`
	Conflicts []model.EditConflict `json:"conflicts"`
}

type RevisionReviewInput struct {
	ReviewNotes string `json:"review_notes" example:"内容已核验，可合并"`
}

type RevisionRevertInput struct {
	EditSummary string `json:"edit_summary" example:"回滚到稳定版本"`
}

type SongAnnotationInput struct {
	LineNumber int    `json:"line_number" binding:"required" example:"12"`
	Content    string `json:"content" binding:"required" example:"这一句在致敬某段旋律。"`
}

type SongAnnotationUpdateInput struct {
	Content string `json:"content" binding:"required" example:"更新后的注释内容。"`
}

type SongAnnotationGroupListResponse struct {
	Data []AnnotationGroup `json:"data"`
}

type SongAnnotationResponse struct {
	Data model.LyricAnnotation `json:"data"`
}

type EntryStatusInput struct {
	Status string `json:"status" binding:"required" example:"confirmed"`
	Reason string `json:"reason" example:"资料已核实"`
}

type EntryStatusResponse struct {
	Message string `json:"message" example:"Album entry status updated"`
	Status  string `json:"status" example:"confirmed"`
}

type MusicEntryListResponse struct {
	Data     []MusicEntryItem `json:"data"`
	Total    int64            `json:"total" example:"42"`
	Page     int              `json:"page" example:"1"`
	PageSize int              `json:"page_size" example:"20"`
}

type ArtistCorrectionInput struct {
	ArtistID    string `json:"artist_id" format:"uuid" binding:"required" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Description string `json:"description" binding:"required" example:"补充艺人生平与成员信息"`
	Reason      string `json:"reason" example:"现有资料过少"`
}

type CorrectionSubmissionResponse struct {
	Message string `json:"message" example:"Song correction submitted successfully"`
	ID      string `json:"id" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Status  string `json:"status" example:"pending"`
}

type ArtistCorrectionResponse struct {
	Data model.ArtistCorrection `json:"data"`
}

type UploadURLResponse struct {
	URL string `json:"url" example:"https://cdn.example.com/uploads/file.png"`
}

type VideoCreateInput struct {
	ChannelID     string   `json:"channel_id,omitempty" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Title         string   `json:"title" binding:"required" example:"发布会录像"`
	Description   string   `json:"description" example:"完整回放"`
	StorageType   string   `json:"storage_type" example:"external"`
	VideoURL      string   `json:"video_url" binding:"required" example:"https://video.example.com/watch.mp4"`
	ThumbnailURL  string   `json:"thumbnail_url" example:"https://cdn.example.com/cover.jpg"`
	DurationSec   int      `json:"duration_sec" example:"3600"`
	Visibility    string   `json:"visibility" example:"public"`
	Status        string   `json:"status" example:"draft"`
	Tags          []string `json:"tags" example:"release,live"`
	CollectionIDs []string `json:"collection_ids" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8b"`
}

type VideoUpdateInput struct {
	ChannelID     string   `json:"channel_id,omitempty" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Title         string   `json:"title" example:"更新后的标题"`
	Description   string   `json:"description" example:"更新后的描述"`
	ThumbnailURL  string   `json:"thumbnail_url" example:"https://cdn.example.com/new-cover.jpg"`
	Visibility    string   `json:"visibility" example:"followers"`
	Status        string   `json:"status" example:"published"`
	Tags          []string `json:"tags" example:"release,edited"`
	CollectionIDs []string `json:"collection_ids" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8b"`
}

type PodcastEpisodeCreateInput struct {
	ChannelID       string `json:"channel_id" binding:"required" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Title           string `json:"title" binding:"required" example:"第 1 期"`
	Shownotes       string `json:"shownotes" example:"本期讨论内容"`
	AudioURL        string `json:"audio_url" binding:"required" example:"https://cdn.example.com/episode.mp3"`
	DurationSec     int    `json:"duration_sec" example:"1800"`
	EpisodeCoverURL string `json:"episode_cover_url" example:"https://cdn.example.com/episode.jpg"`
	SeasonNumber    int    `json:"season_number" example:"1"`
	EpisodeNumber   int    `json:"episode_number" example:"3"`
	Status          string `json:"status" example:"draft"`
}

type PodcastEpisodeUpdateInput struct {
	Title           string `json:"title" example:"更新后的标题"`
	Shownotes       string `json:"shownotes" example:"更新后的 shownotes"`
	EpisodeCoverURL string `json:"episode_cover_url" example:"https://cdn.example.com/episode-new.jpg"`
	DurationSec     int    `json:"duration_sec" example:"2100"`
	SeasonNumber    int    `json:"season_number" example:"1"`
	EpisodeNumber   int    `json:"episode_number" example:"4"`
	Status          string `json:"status" example:"published"`
}

type ShowEpisodesResponse struct {
	Channel  model.Channel          `json:"channel"`
	Episodes []model.PodcastEpisode `json:"episodes"`
}

type SubscriptionResponse struct {
	Data    model.Subscription `json:"data"`
	Message string             `json:"message" example:"ok"`
}

type SubscriptionListResponse struct {
	Data    []model.Subscription `json:"data"`
	Message string               `json:"message" example:"ok"`
}

type TimelineResponse struct {
	Data    []TimelineItem `json:"data"`
	Total   int            `json:"total" example:"20"`
	Page    int            `json:"page" example:"1"`
	Limit   int            `json:"limit" example:"20"`
	Message string         `json:"message" example:"ok"`
}

type FeedStatsResponse struct {
	Data    FeedStatsData `json:"data"`
	Message string        `json:"message" example:"ok"`
}

type SubscriptionGroupResponse struct {
	Data    model.SubscriptionGroup `json:"data"`
	Message string                  `json:"message" example:"ok"`
}

type SubscriptionGroupListResponse struct {
	Data    []model.SubscriptionGroup `json:"data"`
	Message string                    `json:"message" example:"ok"`
}

type OPMLImportResponse struct {
	Message  string `json:"message" example:"OPML import completed"`
	Imported int    `json:"imported" example:"12"`
	Failed   int    `json:"failed" example:"1"`
}

type StarToggleInput struct {
	FeedItemID string `json:"feed_item_id" format:"uuid" binding:"required" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
}

type StarToggleResponse struct {
	Starred bool   `json:"starred" example:"true"`
	Message string `json:"message" example:"Item starred"`
}

type StarredFeedItem struct {
	ID             string `json:"id" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Title          string `json:"title" example:"An interesting post"`
	Link           string `json:"link" example:"https://example.com/post"`
	Summary        string `json:"summary" example:"Summary text"`
	Author         string `json:"author" example:"Fafa"`
	PublishedAt    string `json:"published_at" format:"date-time" example:"2026-05-25T11:00:00Z"`
	SourceTitle    string `json:"source_title" example:"My Feed"`
	SourceSiteURL  string `json:"source_site_url" example:"https://example.com/feed.xml"`
	SourceImageURL string `json:"source_image_url" example:"https://example.com/logo.png"`
}

type StarredItemsResponse struct {
	Items []StarredFeedItem `json:"items"`
	Page  int               `json:"page" example:"1"`
	Total int               `json:"total" example:"20"`
}

type SaveToggleResponse struct {
	Saved bool `json:"saved" example:"true"`
}

type ReadingListResponse struct {
	Items []model.ReadingListItem `json:"items"`
	Page  int                     `json:"page" example:"1"`
	Total int64                   `json:"total" example:"12"`
}

type RemoveStatusResponse struct {
	Removed bool `json:"removed" example:"true"`
}

type FeedHealthCheckResponse struct {
	SubscriptionID string `json:"subscription_id" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	HealthStatus   string `json:"health_status" example:"healthy"`
	ErrorMessage   string `json:"error_message,omitempty" example:""`
	LastChecked    string `json:"last_checked,omitempty" format:"date-time" example:"2026-05-25T11:00:00Z"`
	Skipped        bool   `json:"skipped,omitempty" example:"true"`
	Reason         string `json:"reason,omitempty" example:"internal subscription — no external URL to check"`
}

type FeedHealthCheckListResponse struct {
	CheckedCount int                       `json:"checked_count" example:"5"`
	Results      []FeedHealthCheckResponse `json:"results"`
}

type SearchSubscriptionsResponse struct {
	Data  []model.Subscription `json:"data"`
	Count int                  `json:"count" example:"5"`
}

type FeedItemResponse struct {
	Data FeedItemDetailResponse `json:"data"`
}

type SubscriptionActionResponse struct {
	Message      string             `json:"message" example:"Subscribed successfully"`
	Subscription model.Subscription `json:"subscription"`
}

type SubscriptionStatusResponse struct {
	Subscribed   bool               `json:"subscribed" example:"true"`
	Subscription model.Subscription `json:"subscription,omitempty"`
}

type TimelineEventResponse struct {
	Data model.TimelineEvent `json:"data"`
}

type TimelineEventListResponse struct {
	Data  []model.TimelineEvent `json:"data"`
	Total int64                 `json:"total" example:"42"`
	Page  int                   `json:"page" example:"1"`
	Limit int                   `json:"limit" example:"20"`
}

type TimelinePersonResponse struct {
	Data model.TimelinePerson `json:"data"`
}

type TimelinePersonListResponse struct {
	Data  []model.TimelinePerson `json:"data"`
	Total int64                  `json:"total" example:"18"`
	Page  int                    `json:"page" example:"1"`
	Limit int                    `json:"limit" example:"20"`
}

type PersonLocationResponse struct {
	Data model.PersonLocation `json:"data"`
}

type PersonLocationListResponse struct {
	Data []model.PersonLocation `json:"data"`
}

type TimelineRevisionListResponse struct {
	Data []model.TimelineRevision `json:"data"`
}

type DebateResponse struct {
	Data model.Debate `json:"data"`
}

type DebateListResponse struct {
	Data  []model.Debate `json:"data"`
	Total int64          `json:"total" example:"20"`
	Page  int            `json:"page" example:"1"`
	Limit int            `json:"limit" example:"20"`
}

type DebateSearchResponse struct {
	Data []model.Debate `json:"data"`
}

type DebateConcludeInput struct {
	ConclusionType    string `json:"conclusion_type" binding:"required" example:"yes"`
	ConclusionSummary string `json:"conclusion_summary" example:"主要证据已形成共识。"`
}

type DebateConcludeVoteResponse struct {
	ConcludeVoteCount int  `json:"conclude_vote_count" example:"3"`
	ConcludeThreshold int  `json:"conclude_threshold" example:"10"`
	AutoConcluded     bool `json:"auto_concluded" example:"false"`
}

type DebateArgumentResponse struct {
	Data model.Argument `json:"data"`
}

type DebateArgumentListResponse struct {
	Data      []model.Argument `json:"data"`
	UserVotes map[string]int   `json:"user_votes"`
}

type DebateVoteListResponse struct {
	Data []model.DebateVote `json:"data"`
}

type FoldArgumentInput struct {
	FoldNote string `json:"fold_note" example:"证据不足，暂时折叠。"`
}
