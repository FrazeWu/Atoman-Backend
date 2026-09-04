package model

import (
	"time"

	"github.com/google/uuid"
)

const (
	BookLifecycleStatusDraft   = "draft"
	BookLifecycleStatusActive  = "active"
	BookLifecycleStatusRetired = "retired"
	BookLifecycleStatusMerged  = "merged"

	BookEditStatusDevelopment = "development"
	BookEditStatusLocked      = "locked"
	BookEditStatusClosed      = "closed"

	BookImportStatusPendingUpload = "pending_upload"
	BookImportStatusUploading     = "uploading"
	BookImportStatusCompleting    = "completing"
	BookImportStatusUploaded      = "uploaded"
	BookImportStatusScanning      = "scanning"
	BookImportStatusMetadataReady = "metadata_ready"
	BookImportStatusFailed        = "failed"
	BookImportStatusCancelled     = "cancelled"
	BookImportStatusDeleted       = "deleted"

	BookAssetStatusPendingUpload        = "pending_upload"
	BookAssetStatusUploading            = "uploading"
	BookAssetStatusUploaded             = "uploaded"
	BookAssetStatusScanning             = "scanning"
	BookAssetStatusProcessing           = "processing"
	BookAssetStatusMetadataReady        = "metadata_ready"
	BookAssetStatusPrivateAvailable     = "private_available"
	BookAssetStatusPublicationRequested = "publication_requested"
	BookAssetStatusPendingReview        = "pending_review"
	BookAssetStatusFailed               = "failed"
	BookAssetStatusRejected             = "rejected"
	BookAssetStatusQuarantined          = "quarantined"
	BookAssetStatusRemoved              = "removed"

	BookEditTypeCreate = "create"
	BookEditTypeUpdate = "update"
	BookEditTypeMerge  = "merge"
	BookEditTypeRetire = "retire"
	BookEditTypeReopen = "reopen"

	BookEditStatusPending   = "pending"
	BookEditStatusApproved  = "approved"
	BookEditStatusRejected  = "rejected"
	BookEditStatusWithdrawn = "withdrawn"

	BookShelfStatusWantToRead = "want_to_read"
	BookShelfStatusReading    = "reading"
	BookShelfStatusRead       = "read"
	BookShelfStatusOnHold     = "on_hold"
	BookShelfStatusDropped    = "dropped"

	BookPublicationStatusPendingReview = "pending_review"
	BookPublicationStatusPublished     = "published"
	BookPublicationStatusRejected      = "rejected"
	BookPublicationStatusQuarantined   = "quarantined"
	BookPublicationStatusRemoved       = "removed"

	BookReviewVisibilityPublic  = "public"
	BookReviewVisibilityPrivate = "private"

	BookEditVoteUp   = 1
	BookEditVoteDown = -1

	BookPublicationReportStatusPending  = "pending"
	BookPublicationReportStatusRejected = "rejected"
	BookPublicationReportStatusRemoved  = "removed"

	BookPublicationAppealStatusPending  = "pending"
	BookPublicationAppealStatusApproved = "approved"
	BookPublicationAppealStatusRejected = "rejected"
)

// BookWork is the public bibliographic work layer. It never contains book text.
type BookWork struct {
	Base
	Title           string     `json:"title" gorm:"not null"`
	Subtitle        string     `json:"subtitle,omitempty"`
	OriginalTitle   string     `json:"original_title,omitempty"`
	Description     string     `json:"description,omitempty" gorm:"type:text"`
	Language        string     `json:"language,omitempty" gorm:"index"`
	LifecycleStatus string     `json:"lifecycle_status" gorm:"not null;default:'draft';index;check:chk_book_works_lifecycle_status,lifecycle_status IN ('draft','active','retired','merged')"`
	EditStatus      string     `json:"edit_status" gorm:"not null;default:'development';index;check:chk_book_works_edit_status,edit_status IN ('development','locked','closed')"`
	CreatedBy       *uuid.UUID `json:"created_by,omitempty" gorm:"type:uuid;index"`
	RedirectTo      *uuid.UUID `json:"redirect_to,omitempty" gorm:"type:uuid;index"`
	RatingScore     float64    `json:"rating_score" gorm:"-"`
	RatingCount     int64      `json:"rating_count" gorm:"-"`
}

func (BookWork) TableName() string { return "book_works" }

// BookEdition is a public edition record. Public metadata is independent from private assets.
type BookEdition struct {
	Base
	WorkID          uuid.UUID  `json:"work_id" gorm:"type:uuid;not null;index"`
	Title           string     `json:"title,omitempty"`
	Publisher       string     `json:"publisher,omitempty"`
	ISBN10          string     `json:"isbn10,omitempty" gorm:"index"`
	ISBN13          string     `json:"isbn13,omitempty" gorm:"index"`
	Language        string     `json:"language,omitempty"`
	PublishedDate   *time.Time `json:"published_date,omitempty" gorm:"type:date"`
	PageCount       int        `json:"page_count,omitempty"`
	Binding         string     `json:"binding,omitempty"`
	CoverURL        string     `json:"cover_url,omitempty" gorm:"type:text"`
	CoverSource     string     `json:"cover_source,omitempty"`
	LifecycleStatus string     `json:"lifecycle_status" gorm:"not null;default:'draft';index;check:chk_book_editions_lifecycle_status,lifecycle_status IN ('draft','active','retired','merged')"`
	EditStatus      string     `json:"edit_status" gorm:"not null;default:'development';index;check:chk_book_editions_edit_status,edit_status IN ('development','locked','closed')"`
	CreatedBy       *uuid.UUID `json:"created_by,omitempty" gorm:"type:uuid;index"`
	RedirectTo      *uuid.UUID `json:"redirect_to,omitempty" gorm:"type:uuid;index"`
}

func (BookEdition) TableName() string { return "book_editions" }

type BookPerson struct {
	Base
	Name            string     `json:"name" gorm:"not null;index"`
	SortName        string     `json:"sort_name,omitempty" gorm:"index"`
	Description     string     `json:"description,omitempty" gorm:"type:text"`
	LifecycleStatus string     `json:"lifecycle_status" gorm:"not null;default:'draft';index;check:chk_book_people_lifecycle_status,lifecycle_status IN ('draft','active','retired','merged')"`
	CreatedBy       *uuid.UUID `json:"created_by,omitempty" gorm:"type:uuid;index"`
	RedirectTo      *uuid.UUID `json:"redirect_to,omitempty" gorm:"type:uuid;index"`
}

func (BookPerson) TableName() string { return "book_people" }

type BookContribution struct {
	Base
	WorkID    *uuid.UUID `json:"work_id,omitempty" gorm:"type:uuid;index;uniqueIndex:idx_book_contributions_target,priority:1"`
	EditionID *uuid.UUID `json:"edition_id,omitempty" gorm:"type:uuid;index;uniqueIndex:idx_book_contributions_target,priority:2"`
	PersonID  uuid.UUID  `json:"person_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_book_contributions_target,priority:3"`
	Role      string     `json:"role" gorm:"not null;uniqueIndex:idx_book_contributions_target,priority:4"`
	Position  int        `json:"position" gorm:"not null;default:1"`
}

func (BookContribution) TableName() string { return "book_contributions" }

// BookSource records an auditable source for a work, edition, or contribution edit.
type BookSource struct {
	Base
	TargetType  string     `json:"target_type" gorm:"not null;index"`
	TargetID    uuid.UUID  `json:"target_id" gorm:"type:uuid;not null;index"`
	BookEditID  *uuid.UUID `json:"book_edit_id,omitempty" gorm:"type:uuid;index"`
	Kind        string     `json:"kind,omitempty" gorm:"not null;default:'bibliographic'"`
	Title       string     `json:"title,omitempty"`
	URL         string     `json:"url" gorm:"type:text;not null"`
	Note        string     `json:"note,omitempty" gorm:"type:text"`
	SubmittedBy *uuid.UUID `json:"submitted_by,omitempty" gorm:"type:uuid;index"`
}

func (BookSource) TableName() string { return "book_sources" }

type BookEdit struct {
	Base
	Type         string     `json:"type" gorm:"not null;index"`
	EntityType   string     `json:"entity_type" gorm:"not null;index"`
	EntityID     *uuid.UUID `json:"entity_id,omitempty" gorm:"type:uuid;index"`
	SubmittedBy  uuid.UUID  `json:"submitted_by" gorm:"type:uuid;not null;index"`
	Status       string     `json:"status" gorm:"not null;default:'pending';index;check:chk_book_edits_status,status IN ('pending','approved','rejected','withdrawn')"`
	PayloadJSON  string     `json:"-" gorm:"type:jsonb;not null;default:'{}'"`
	ChangesJSON  string     `json:"-" gorm:"type:jsonb;not null;default:'{}'"`
	Reason       string     `json:"reason,omitempty" gorm:"type:text"`
	ReviewerID   *uuid.UUID `json:"reviewer_id,omitempty" gorm:"type:uuid;index"`
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`
	DecisionNote string     `json:"decision_note,omitempty" gorm:"type:text"`
}

func (BookEdit) TableName() string { return "book_edits" }

// UserBookImport is private metadata and processing state owned by one user.
type UserBookImport struct {
	Base
	UserID             uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	Title              string     `json:"-" gorm:"type:text;not null"`
	Author             string     `json:"-" gorm:"type:text;not null;default:''"`
	OriginalFilename   string     `json:"-" gorm:"type:text;not null;default:''"`
	Format             string     `json:"-" gorm:"not null;default:''"`
	ContentType        string     `json:"-" gorm:"not null;default:''"`
	SizeBytes          int64      `json:"-" gorm:"not null;default:0"`
	ObjectKey          string     `json:"-" gorm:"type:text;not null;default:''"`
	UploadID           string     `json:"-" gorm:"type:text;not null;default:''"`
	PartSize           int64      `json:"-" gorm:"not null;default:0"`
	CompletedPartsJSON string     `json:"-" gorm:"type:jsonb;not null;default:'[]'"`
	ExpiresAt          time.Time  `json:"-" gorm:"index"`
	CompletedAt        *time.Time `json:"-"`
	MetadataJSON       string     `json:"-" gorm:"type:jsonb;not null;default:'{}'"`
	Status             string     `json:"-" gorm:"not null;default:'pending_upload';index;check:chk_user_book_imports_status,status IN ('pending_upload','uploading','completing','uploaded','scanning','metadata_ready','failed','cancelled','deleted')"`
	WorkID             *uuid.UUID `json:"-" gorm:"type:uuid;index"`
	EditionID          *uuid.UUID `json:"-" gorm:"type:uuid;index"`
	ErrorCode          string     `json:"-" gorm:"not null;default:''"`
	ErrorMessage       string     `json:"-" gorm:"type:text;not null;default:''"`
}

func (UserBookImport) TableName() string { return "user_book_imports" }

// UserBookAsset contains private source and derived object references.
type UserBookAsset struct {
	Base
	ImportID         uuid.UUID `json:"-" gorm:"type:uuid;not null;uniqueIndex:idx_user_book_assets_import_id;index"`
	UserID           uuid.UUID `json:"-" gorm:"type:uuid;not null;index"`
	OriginalFilename string    `json:"-" gorm:"type:text;not null"`
	ContentType      string    `json:"-" gorm:"not null"`
	Format           string    `json:"-" gorm:"not null;index;check:chk_user_book_assets_format,format IN ('epub','pdf','txt')"`
	SizeBytes        int64     `json:"-" gorm:"not null"`
	SHA256           string    `json:"-" gorm:"type:varchar(64);not null;index"`
	ObjectKey        string    `json:"-" gorm:"type:text;not null"`
	DerivedObjectKey string    `json:"-" gorm:"type:text;not null;default:''"`
	ScanStatus       string    `json:"-" gorm:"not null;default:'pending';index"`
	ProcessingStatus string    `json:"-" gorm:"not null;default:'pending_upload';index;check:chk_user_book_assets_processing_status,processing_status IN ('pending_upload','uploading','uploaded','scanning','processing','metadata_ready','private_available','publication_requested','pending_review','failed','rejected','quarantined','removed')"`
	ErrorMessage     string    `json:"-" gorm:"type:text;not null;default:''"`
}

func (UserBookAsset) TableName() string { return "user_book_assets" }

// UserBookReadingState is private per-user reading position and notes.
type UserBookReadingState struct {
	Base
	UserID          uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	AssetID         uuid.UUID  `json:"-" gorm:"type:uuid;not null;uniqueIndex:idx_user_book_reading_states_asset"`
	EPUBCFI         string     `json:"-" gorm:"type:text;not null;default:''"`
	PDFPage         int        `json:"-" gorm:"not null;default:0"`
	TXTOffset       int64      `json:"-" gorm:"not null;default:0"`
	ReadingPercent  float64    `json:"-" gorm:"not null;default:0"`
	LastReadAt      *time.Time `json:"-" gorm:"index"`
	PrivateNotes    string     `json:"-" gorm:"type:text;not null;default:''"`
	PreferencesJSON string     `json:"-" gorm:"type:jsonb;not null;default:'{}'"`
}

func (UserBookReadingState) TableName() string { return "user_book_reading_states" }

type UserBookShelf struct {
	Base
	UserID uuid.UUID `json:"-" gorm:"type:uuid;not null;index;uniqueIndex:idx_user_book_shelves_user_work,priority:1"`
	WorkID uuid.UUID `json:"-" gorm:"type:uuid;not null;index;uniqueIndex:idx_user_book_shelves_user_work,priority:2"`
	Status string    `json:"-" gorm:"not null;default:'want_to_read';index;check:chk_user_book_shelves_status,status IN ('want_to_read','reading','read','on_hold','dropped')"`
	Note   string    `json:"-" gorm:"type:text;not null;default:''"`
}

func (UserBookShelf) TableName() string { return "user_book_shelves" }

type BookPublicationRequest struct {
	Base
	SubmittedBy         uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	AssetID             uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	WorkID              *uuid.UUID `json:"-" gorm:"type:uuid;index"`
	EditionID           *uuid.UUID `json:"-" gorm:"type:uuid;index"`
	Status              string     `json:"status" gorm:"not null;default:'pending_review';index;check:chk_book_publication_requests_status,status IN ('pending_review','published','rejected','quarantined','removed')"`
	Reason              string     `json:"-" gorm:"type:text;not null;default:''"`
	ReviewerID          *uuid.UUID `json:"-" gorm:"type:uuid;index"`
	ReviewedAt          *time.Time `json:"-"`
	DecisionNote        string     `json:"-" gorm:"type:text;not null;default:''"`
	RetentionHold       bool       `json:"-" gorm:"not null;default:false;index"`
	RetentionHoldReason string     `json:"-" gorm:"type:text;not null;default:''"`
	RetentionHoldSetBy  *uuid.UUID `json:"-" gorm:"type:uuid;index"`
	RetentionHoldSetAt  *time.Time `json:"-"`
}

func (BookPublicationRequest) TableName() string { return "book_publication_requests" }

type BookRightsDeclaration struct {
	Base
	RequestID           uuid.UUID  `json:"-" gorm:"type:uuid;not null;uniqueIndex;index"`
	LicenseType         string     `json:"-" gorm:"not null;index;check:chk_book_rights_declarations_license_type,license_type IN ('public_domain','open_license','creator_owned','authorized_distribution')"`
	RightsHolder        string     `json:"-" gorm:"type:text;not null;default:''"`
	SourceURL           string     `json:"-" gorm:"type:text;not null;default:''"`
	Declaration         string     `json:"-" gorm:"type:text;not null"`
	EvidenceObjectKey   string     `json:"-" gorm:"type:text;not null;default:''"`
	EvidenceFileName    string     `json:"-" gorm:"type:text;not null;default:''"`
	EvidenceContentType string     `json:"-" gorm:"type:varchar(255);not null;default:''"`
	EvidenceSizeBytes   int64      `json:"-" gorm:"not null;default:0"`
	EvidenceDeletedAt   *time.Time `json:"-" gorm:"index"`
	ReviewConclusion    string     `json:"-" gorm:"type:text;not null;default:''"`
	ReviewedBy          *uuid.UUID `json:"-" gorm:"type:uuid;index"`
	ReviewedAt          *time.Time `json:"-"`
}

func (BookRightsDeclaration) TableName() string { return "book_rights_declarations" }

// PublishedBookAsset is created independently after a publication request passes review.
type PublishedBookAsset struct {
	Base
	PublicationRequestID uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	SourceAssetID        uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	WorkID               *uuid.UUID `json:"work_id,omitempty" gorm:"type:uuid;index"`
	EditionID            *uuid.UUID `json:"edition_id,omitempty" gorm:"type:uuid;index"`
	Format               string     `json:"format" gorm:"not null;check:chk_published_book_assets_format,format IN ('epub','pdf','txt')"`
	ObjectKey            string     `json:"-" gorm:"type:text;not null"`
	SHA256               string     `json:"-" gorm:"type:varchar(64);not null;index"`
	Status               string     `json:"status" gorm:"not null;default:'pending_review';index;check:chk_published_book_assets_status,status IN ('pending_review','published','rejected','quarantined','removed')"`
	RemovedAt            *time.Time `json:"removed_at,omitempty"`
	RemovalReason        string     `json:"-" gorm:"type:text;not null;default:''"`
}

func (PublishedBookAsset) TableName() string { return "published_book_assets" }

type BookRating struct {
	Base
	UserID uuid.UUID `json:"-" gorm:"type:uuid;not null;index;uniqueIndex:idx_book_ratings_user_work,priority:1"`
	WorkID uuid.UUID `json:"work_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_book_ratings_user_work,priority:2"`
	Score  int       `json:"score" gorm:"not null;check:chk_book_ratings_score,score BETWEEN 1 AND 10"`
}

func (BookRating) TableName() string { return "book_ratings" }

type BookReview struct {
	Base
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_book_reviews_user_work,priority:1"`
	WorkID     uuid.UUID `json:"work_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_book_reviews_user_work,priority:2"`
	Content    string    `json:"content" gorm:"type:text;not null"`
	Spoiler    bool      `json:"spoiler" gorm:"not null;default:false"`
	Visibility string    `json:"visibility" gorm:"not null;default:'public';index;check:chk_book_reviews_visibility,visibility IN ('public','private')"`
}

func (BookReview) TableName() string { return "book_reviews" }

type BookPostLink struct {
	Base
	WorkID uuid.UUID `json:"work_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_book_post_links_work_post,priority:1"`
	PostID uuid.UUID `json:"post_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_book_post_links_work_post,priority:2"`
}

func (BookPostLink) TableName() string { return "book_post_links" }

type BookEditVote struct {
	Base
	EditID uuid.UUID `json:"edit_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_book_edit_votes_edit_user,priority:1"`
	UserID uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_book_edit_votes_edit_user,priority:2"`
	Value  int       `json:"value" gorm:"not null;check:chk_book_edit_votes_value,value IN (-1,1)"`
}

func (BookEditVote) TableName() string { return "book_edit_votes" }

type BookPublicationReport struct {
	Base
	AssetID      uuid.UUID  `json:"asset_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_book_publication_reports_asset_user,priority:1"`
	ReporterID   uuid.UUID  `json:"reporter_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_book_publication_reports_asset_user,priority:2"`
	Reason       string     `json:"reason" gorm:"type:text;not null"`
	Status       string     `json:"status" gorm:"not null;default:'pending';index;check:chk_book_publication_reports_status,status IN ('pending','rejected','removed')"`
	ReviewerID   *uuid.UUID `json:"reviewer_id,omitempty" gorm:"type:uuid;index"`
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`
	DecisionNote string     `json:"decision_note,omitempty" gorm:"type:text"`
}

type BookPublicationAppeal struct {
	Base
	PublicationRequestID uuid.UUID  `json:"publication_request_id" gorm:"type:uuid;not null;index"`
	PublishedAssetID     uuid.UUID  `json:"published_asset_id" gorm:"type:uuid;not null;index"`
	SubmittedBy          uuid.UUID  `json:"submitted_by" gorm:"type:uuid;not null;index"`
	Reason               string     `json:"reason" gorm:"type:text;not null"`
	Status               string     `json:"status" gorm:"not null;default:'pending';index;check:chk_book_publication_appeals_status,status IN ('pending','approved','rejected')"`
	ReviewerID           *uuid.UUID `json:"reviewer_id,omitempty" gorm:"type:uuid;index"`
	ReviewedAt           *time.Time `json:"reviewed_at,omitempty"`
	DecisionNote         string     `json:"decision_note,omitempty" gorm:"type:text"`
}

func (BookPublicationAppeal) TableName() string { return "book_publication_appeals" }
