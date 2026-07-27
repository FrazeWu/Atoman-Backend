package dm

import "errors"

var (
	ErrTargetNotFound        = errors.New("dm target not found")
	ErrSelfTarget            = errors.New("dm self target")
	ErrPermissionDenied      = errors.New("dm permission denied")
	ErrWaitingReply          = errors.New("dm waiting reply")
	ErrBlocked               = errors.New("dm blocked")
	ErrConversationForbidden = errors.New("dm conversation forbidden")
	ErrImageInvalid          = errors.New("dm image invalid")
	ErrRateLimited           = errors.New("dm rate limited")
	ErrMessageNotFound       = errors.New("dm message not found")
	ErrAlreadyReported       = errors.New("dm already reported")
	ErrStorageUnavailable    = errors.New("dm storage unavailable")
)
