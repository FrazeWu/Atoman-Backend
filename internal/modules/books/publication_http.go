package books

import ()

type PublishedBookAssetListResult struct {
	Items  []BookPublishedAssetDTO `json:"items"`
	Total  int64                   `json:"total"`
	Limit  int                     `json:"limit"`
	Offset int                     `json:"offset"`
}
