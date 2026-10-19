package books

import ()

type BookPostListResult struct {
	Items  []BookPublicPostDTO `json:"items"`
	Total  int64               `json:"total"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
}
