package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/go-api-template/internal/dictionary"
	platformstableid "github.com/lihongjie0209/microservice-platform-go/stableid"
)

const DictionaryCode = "platform.application"
const DictionaryNamespace = "01212657-059e-46ef-ad7b-5ac822c2b050"

func DictionaryDefinitionID() (string, error) {
	generator, err := platformstableid.New(DictionaryNamespace)
	if err != nil {
		return "", err
	}
	return generator.String("dictionary:" + DictionaryCode)
}

type DictionaryProvider struct{ db *sqlx.DB }

func NewDictionaryProvider(db *sqlx.DB) *DictionaryProvider { return &DictionaryProvider{db: db} }

func RegisterDictionaryProvider(registry *dictionary.ProviderRegistry, provider *DictionaryProvider) error {
	return registry.Register(DictionaryCode, provider)
}

func (p *DictionaryProvider) Query(ctx context.Context, query dictionary.Query) (dictionary.Result, error) {
	page, pageSize := query.Page, query.PageSize
	if page == 0 {
		page = 1
	}
	if pageSize == 0 {
		pageSize = 20
	}
	where, args := []string{"deleted_at IS NULL"}, []any{}
	if !query.IncludeDisabled {
		where = append(where, "status='active'")
	}
	if query.Keyword != "" {
		where = append(where, "(LOWER(code) LIKE LOWER(?) OR LOWER(name) LIKE LOWER(?))")
		value := "%" + query.Keyword + "%"
		args = append(args, value, value)
	}
	var err error
	where, args, err = appendDictionaryIn(where, args, "id", query.IDs)
	if err == nil {
		where, args, err = appendDictionaryIn(where, args, "code", query.Codes)
	}
	if err != nil {
		return dictionary.Result{}, err
	}
	order, err := dictionaryOrder(query.Sort)
	if err != nil {
		return dictionary.Result{}, err
	}
	clause := strings.Join(where, " AND ")
	var total int64
	if err := p.db.GetContext(ctx, &total, p.db.Rebind(`SELECT count(*) FROM applications WHERE `+clause), args...); err != nil {
		return dictionary.Result{}, err
	}
	var rows []struct {
		ID        string `db:"id"`
		Code      string `db:"code"`
		Name      string `db:"name"`
		Icon      string `db:"icon"`
		HomePath  string `db:"home_path"`
		Status    string `db:"status"`
		SortOrder int64  `db:"sort_order"`
	}
	pageArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	if err := p.db.SelectContext(ctx, &rows, p.db.Rebind(`SELECT id,code,name,icon,home_path,status,sort_order FROM applications WHERE `+clause+` ORDER BY `+order+` LIMIT ? OFFSET ?`), pageArgs...); err != nil {
		return dictionary.Result{}, err
	}
	items := make([]dictionary.ResultItem, 0, len(rows))
	for _, row := range rows {
		extension, _ := json.Marshal(map[string]any{"icon": row.Icon, "home_path": row.HomePath, "status": row.Status})
		items = append(items, dictionary.ResultItem{ID: row.ID, Code: row.Code, Name: row.Name, Value: row.ID, Disabled: row.Status != "active", SortOrder: row.SortOrder, Extension: extension, Children: []dictionary.ResultItem{}})
	}
	return dictionary.Result{Code: DictionaryCode, Name: "应用", Type: dictionary.KindEnum, Items: items, Page: page, PageSize: pageSize, Total: total, Extension: json.RawMessage(`{}`)}, nil
}

func dictionaryOrder(sorts []dictionary.Sort) (string, error) {
	if len(sorts) == 0 {
		return "sort_order ASC,id ASC", nil
	}
	allowed := map[string]string{"sort_order": "sort_order", "code": "code", "name": "name"}
	parts := make([]string, 0, len(sorts)+1)
	seen := map[string]struct{}{}
	for _, sort := range sorts {
		column, ok := allowed[sort.Field]
		if !ok || (sort.Direction != "asc" && sort.Direction != "desc") {
			return "", fmt.Errorf("%w: invalid application dictionary sort", dictionary.ErrInvalid)
		}
		if _, duplicate := seen[column]; duplicate {
			return "", fmt.Errorf("%w: duplicate application dictionary sort", dictionary.ErrInvalid)
		}
		seen[column] = struct{}{}
		parts = append(parts, column+" "+strings.ToUpper(sort.Direction))
	}
	return strings.Join(append(parts, "id ASC"), ","), nil
}

func appendDictionaryIn(where []string, args []any, column string, values []string) ([]string, []any, error) {
	if len(values) == 0 {
		return where, args, nil
	}
	placeholders := make([]string, len(values))
	for i, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > 128 {
			return nil, nil, fmt.Errorf("%w: invalid application dictionary filter", dictionary.ErrInvalid)
		}
		placeholders[i], args = "?", append(args, value)
	}
	return append(where, column+" IN ("+strings.Join(placeholders, ",")+")"), args, nil
}
