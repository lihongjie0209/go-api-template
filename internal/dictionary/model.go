package dictionary

import (
	"encoding/json"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/pagination"
)

type Kind string
type Source string

const (
	KindEnum       Kind   = "enum"
	KindTree       Kind   = "tree"
	SourceStatic   Source = "static"
	SourceProvider Source = "provider"
)

type Definition struct {
	ID            string          `db:"id" json:"id"`
	Code          string          `db:"dictionary_code" json:"code"`
	Name          string          `db:"name" json:"name"`
	Kind          Kind            `db:"dictionary_type" json:"type"`
	Source        Source          `db:"source_type" json:"source"`
	Description   string          `db:"description" json:"description"`
	Status        string          `db:"status" json:"status"`
	Extension     json.RawMessage `db:"extension" json:"extension" swaggertype:"object"`
	CreatedAt     time.Time       `db:"created_at" json:"created_at"`
	CreatedBy     string          `db:"created_by" json:"created_by"`
	CreatedByName string          `db:"-" json:"created_by_name"`
	UpdatedAt     time.Time       `db:"updated_at" json:"updated_at"`
	UpdatedBy     string          `db:"updated_by" json:"updated_by"`
	UpdatedByName string          `db:"-" json:"updated_by_name"`
	Version       int64           `db:"version" json:"version"`
}

type Item struct {
	ID            string          `db:"id" json:"id"`
	DictionaryID  string          `db:"dictionary_id" json:"dictionary_id"`
	ParentID      *string         `db:"parent_id" json:"parent_id,omitempty"`
	Code          string          `db:"code" json:"code"`
	Name          string          `db:"name" json:"name"`
	Value         string          `db:"value" json:"value"`
	Disabled      bool            `db:"disabled" json:"disabled"`
	SortOrder     int64           `db:"sort_order" json:"sort_order"`
	Extension     json.RawMessage `db:"extension" json:"extension" swaggertype:"object"`
	Children      []Item          `db:"-" json:"children,omitempty"`
	CreatedAt     time.Time       `db:"created_at" json:"created_at"`
	CreatedBy     string          `db:"created_by" json:"created_by"`
	CreatedByName string          `db:"-" json:"created_by_name"`
	UpdatedAt     time.Time       `db:"updated_at" json:"updated_at"`
	UpdatedBy     string          `db:"updated_by" json:"updated_by"`
	UpdatedByName string          `db:"-" json:"updated_by_name"`
	Version       int64           `db:"version" json:"version"`
}

type Sort struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}
type Query struct {
	Code            string          `json:"code"`
	Keyword         string          `json:"keyword,omitempty"`
	IDs             []string        `json:"ids,omitempty"`
	Codes           []string        `json:"codes,omitempty"`
	IncludeDisabled bool            `json:"include_disabled"`
	Sort            []Sort          `json:"sort,omitempty"`
	Page            int             `json:"page,omitempty"`
	PageSize        int             `json:"page_size,omitempty"`
	Extension       json.RawMessage `json:"extension,omitempty" swaggertype:"object"`
}
type Result struct {
	Code      string          `json:"code"`
	Name      string          `json:"name"`
	Type      Kind            `json:"type"`
	Items     []ResultItem    `json:"items"`
	Page      int             `json:"page"`
	PageSize  int             `json:"page_size"`
	Total     int64           `json:"total"`
	Extension json.RawMessage `json:"extension" swaggertype:"object"`
}
type ProviderQueryRequest = Query
type ProviderQueryResponse = Result
type ResultItem struct {
	ID        string          `json:"id"`
	ParentID  *string         `json:"parent_id,omitempty"`
	Code      string          `json:"code"`
	Name      string          `json:"name"`
	Value     string          `json:"value"`
	Disabled  bool            `json:"disabled"`
	SortOrder int64           `json:"sort_order"`
	Extension json.RawMessage `json:"extension" swaggertype:"object"`
	Children  []ResultItem    `json:"children,omitempty"`
}

type DefinitionInput struct {
	Code, Name, Description, Status string
	Kind                            Kind
	Source                          Source
	Extension                       json.RawMessage
}
type ItemInput struct {
	DictionaryID      string
	ParentID          *string
	Code, Name, Value string
	Disabled          bool
	SortOrder         int64
	Extension         json.RawMessage
}
type DefinitionUpdate struct {
	ID, Name, Description, Status string
	Extension                     json.RawMessage
	Version                       int64
}
type ItemUpdate struct {
	ID          string
	ParentID    *string
	Name, Value string
	Disabled    bool
	SortOrder   int64
	Extension   json.RawMessage
	Version     int64
}
type Page struct {
	Items    []Definition `json:"items"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
	Total    int64        `json:"total"`
}
type DefinitionPageInput struct {
	pagination.Request
	Types    []Kind
	Sources  []Source
	Statuses []string
}
type ItemPageInput struct {
	pagination.Request
	DictionaryID string
	Disabled     *bool
	ParentID     *string
}
type ItemPage struct {
	Items    []Item `json:"items"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Total    int64  `json:"total"`
}
