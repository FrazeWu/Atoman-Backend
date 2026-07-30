package comment

import (
	"errors"
	"sync"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/reference"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrAuthenticationRequired = errors.New("comment authentication required")
	ErrTargetNotVisible       = errors.New("comment target not visible")
	ErrTargetLocked           = errors.New("comment target locked")
	ErrInvalidContent         = errors.New("invalid comment content")
	ErrInvalidReply           = errors.New("invalid comment reply")
	ErrInvalidAttachment      = errors.New("invalid comment attachment")
	ErrInvalidMention         = errors.New("invalid comment mention")
	ErrInvalidListOptions     = errors.New("invalid comment list options")
	ErrInvalidCommentID       = errors.New("invalid comment ID")
	ErrCommentForbidden       = errors.New("comment operation forbidden")
	ErrCommentNotFound        = errors.New("comment not found")
	ErrInvalidMark            = errors.New("invalid comment mark")
	ErrCommentRateLimited     = errors.New("comment rate limited")
	ErrDuplicateComment       = errors.New("duplicate comment")
)

const (
	CommentStatusActive          = "active"
	CommentStatusAutoFolded      = "auto_folded"
	CommentStatusModeratorHidden = "moderator_hidden"
	commentStatusActive          = CommentStatusActive
)

type ExtensionWriter func(tx *gorm.DB, comment *model.CommentEntry) error
type DeleteExtensionWriter func(tx *gorm.DB, commentIDs []uuid.UUID) error

type Service struct {
	db          *gorm.DB
	registry    *TargetRegistry
	repo        repository
	createMu    *sync.Mutex
	now         func() time.Time
	checkAbuse  bool
	forumPolicy ForumPolicy
	references  *reference.Service
}

func (s *Service) SetForumPolicy(policy ForumPolicy) {
	s.forumPolicy = policy
}

func NewService(db *gorm.DB, registry *TargetRegistry) *Service {
	return &Service{db: db, registry: registry, createMu: createTransactionMutex(db.Dialector.Name()), now: time.Now, checkAbuse: true, references: reference.NewService(db)}
}

func createTransactionMutex(dialect string) *sync.Mutex {
	if dialect == "sqlite" {
		return &sync.Mutex{}
	}
	return nil
}

func withCreateTransactionMutex(mutex *sync.Mutex, transaction func() error) error {
	if mutex == nil {
		return transaction()
	}
	mutex.Lock()
	defer mutex.Unlock()
	return transaction()
}
