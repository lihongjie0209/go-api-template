package pagination

import "errors"

const (
	DefaultPageSize = 20
	MaxPageSize     = 200
	MaxPage         = 1_000_000
)

type Request struct {
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Keyword  string `json:"keyword,omitempty"`
}

// Sort is the shared API shape for allowlisted server-side sorting. Services
// must map Field to a constant SQL identifier and never interpolate it before
// validation.
type Sort struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

type Result[T any] struct {
	Items    []T   `json:"items"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Total    int64 `json:"total"`
}

func Normalize(request Request) (Request, error) {
	if request.Page == 0 {
		request.Page = 1
	}
	if request.PageSize == 0 {
		request.PageSize = DefaultPageSize
	}
	if request.Page < 1 || request.Page > MaxPage || request.PageSize < 1 || request.PageSize > MaxPageSize {
		return Request{}, errors.New("invalid pagination")
	}
	return request, nil
}

func Offset(request Request) int { return (request.Page - 1) * request.PageSize }
