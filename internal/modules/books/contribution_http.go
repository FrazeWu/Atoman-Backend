package books

import ()

type BookEditListResult struct {
	Items  []BookEditDTO `json:"items"`
	Total  int64         `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}
