package service

const (
	FullTextStatusDisabled = "disabled"
	FullTextStatusPending  = "pending"
	FullTextStatusFetching = "fetching"
	FullTextStatusRetry    = "retry"
	FullTextStatusSuccess  = "success"
	FullTextStatusFailed   = "failed"
)

const (
	FullTextErrorInvalidURL       = "invalid_url"
	FullTextErrorSourceDisabled   = "source_disabled"
	FullTextErrorRequestTimeout   = "request_timeout"
	FullTextErrorRequestFailed    = "request_failed"
	FullTextErrorHTTPStatus       = "unsupported_status_code"
	FullTextErrorNonHTML          = "non_html_response"
	FullTextErrorResponseTooLarge = "response_too_large"
	FullTextErrorTooManyRedirects = "too_many_redirects"
	FullTextErrorExtractTooShort  = "extract_too_short"
	FullTextErrorSanitizeEmpty    = "sanitize_empty"
	FullTextErrorSSRFBlocked      = "ssrf_blocked"
)
