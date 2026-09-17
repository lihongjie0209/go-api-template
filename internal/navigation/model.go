package navigation

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/pbac"
	"github.com/lihongjie0209/microservice-platform-go/stableid"
	platformtree "github.com/lihongjie0209/microservice-platform-go/tree"
)

const Namespace = "e37c311f-c151-5a85-b682-dc34cbfeb3a5"

var (
	ErrInvalid       = errors.New("invalid navigation")
	ErrNotFound      = errors.New("navigation not found")
	ErrConflict      = errors.New("navigation conflict")
	keyPattern       = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{1,127}$`)
	pathPattern      = regexp.MustCompile(`^/[A-Za-z0-9_./:-]*$`)
	componentPattern = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
)

type Record struct {
	ID            string          `db:"id" json:"id"`
	ApplicationID string          `db:"application_id" json:"application_id"`
	ParentID      *string         `db:"parent_id" json:"parent_id"`
	Key           string          `db:"navigation_key" json:"navigation_key"`
	Name          string          `db:"name" json:"name"`
	Type          string          `db:"navigation_type" json:"navigation_type"`
	RoutePath     string          `db:"route_path" json:"route_path"`
	Component     string          `db:"component" json:"component"`
	Icon          string          `db:"icon" json:"icon"`
	Resource      string          `db:"resource" json:"resource"`
	Action        string          `db:"action" json:"action"`
	Visible       bool            `db:"visible" json:"visible"`
	Status        string          `db:"status" json:"status"`
	SortOrder     int64           `db:"sort_order" json:"sort_order"`
	Metadata      json.RawMessage `db:"metadata" json:"metadata" swaggertype:"object"`
	CreatedAt     time.Time       `db:"created_at" json:"created_at"`
	CreatedBy     string          `db:"created_by" json:"created_by"`
	CreatedByName string          `db:"-" json:"created_by_name"`
	UpdatedAt     time.Time       `db:"updated_at" json:"updated_at"`
	UpdatedBy     string          `db:"updated_by" json:"updated_by"`
	UpdatedByName string          `db:"-" json:"updated_by_name"`
	Version       int64           `db:"version" json:"version"`
}

type Node struct {
	Record
	Children []*Node `json:"children"`
}

type Input struct {
	ApplicationID, ApplicationCode, Key, Name, Type      string
	ParentID                                             *string
	RoutePath, Component, Icon, Resource, Action, Status string
	Visible                                              bool
	SortOrder                                            int64
	Metadata                                             json.RawMessage
}

func Normalize(input Input) Input {
	input.ApplicationID = strings.TrimSpace(input.ApplicationID)
	input.ApplicationCode = strings.ToLower(strings.TrimSpace(input.ApplicationCode))
	input.Key = strings.ToLower(strings.TrimSpace(input.Key))
	input.Name = strings.TrimSpace(input.Name)
	input.Type = strings.TrimSpace(input.Type)
	input.RoutePath = strings.TrimSpace(input.RoutePath)
	input.Component = strings.TrimSpace(input.Component)
	input.Icon = strings.TrimSpace(input.Icon)
	input.Resource = strings.TrimSpace(input.Resource)
	input.Action = strings.TrimSpace(input.Action)
	input.Status = strings.TrimSpace(input.Status)
	if len(input.Metadata) == 0 {
		input.Metadata = json.RawMessage(`{}`)
	}
	return input
}

func Validate(input Input, resources *pbac.Registry) error {
	input = Normalize(input)
	if input.ApplicationID == "" || !keyPattern.MatchString(input.Key) || input.Name == "" || len(input.Name) > 256 ||
		len(input.Icon) > 256 || (input.Status != "active" && input.Status != "disabled") ||
		input.SortOrder < -1_000_000_000 || input.SortOrder > 1_000_000_000 || len(input.Metadata) > 1<<20 || !validMetadata(input.Metadata) {
		return ErrInvalid
	}
	switch input.Type {
	case "directory":
		if input.Component != "" || input.Resource != "" || input.Action != "" || (input.RoutePath != "" && !validPath(input.RoutePath)) {
			return ErrInvalid
		}
	case "menu":
		if !validPath(input.RoutePath) || !componentPattern.MatchString(input.Component) || strings.Contains(input.Component, "..") || input.Resource == "" || input.Action == "" || resources == nil {
			return ErrInvalid
		}
		if _, _, err := resources.Resolve(input.Resource, input.Action); err != nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func StableID(applicationCode, key string) (string, error) {
	applicationCode = strings.ToLower(strings.TrimSpace(applicationCode))
	key = strings.ToLower(strings.TrimSpace(key))
	if applicationCode == "" || !keyPattern.MatchString(key) {
		return "", ErrInvalid
	}
	generator, err := stableid.New(Namespace)
	if err != nil {
		return "", err
	}
	return generator.String("navigation:" + applicationCode + ":" + key)
}

func Build(records []Record) ([]*Node, error) {
	applicationID := ""
	types := make(map[string]string, len(records))
	for _, record := range records {
		if applicationID == "" {
			applicationID = record.ApplicationID
		}
		if record.ApplicationID == "" || record.ApplicationID != applicationID {
			return nil, ErrInvalid
		}
		types[record.ID] = record.Type
	}
	for _, record := range records {
		if record.ParentID != nil && types[*record.ParentID] != "directory" {
			return nil, ErrInvalid
		}
	}
	forest, err := platformtree.Build(records, func(value Record) string { return value.ID }, func(value Record) (string, bool) {
		if value.ParentID == nil {
			return "", false
		}
		return *value.ParentID, true
	})
	if err != nil {
		return nil, ErrInvalid
	}
	var convert func([]*platformtree.Node[Record]) []*Node
	convert = func(source []*platformtree.Node[Record]) []*Node {
		result := make([]*Node, len(source))
		for index, item := range source {
			result[index] = &Node{Record: item.Value, Children: convert(item.Children)}
		}
		return result
	}
	return convert(forest), nil
}

// PruneVisible returns a new tree containing allowed menus and the directories
// required to reach them. Management-only fields remain unchanged, and the
// input tree is never mutated.
func PruneVisible(nodes []*Node, allowedMenuIDs map[string]struct{}) []*Node {
	result := make([]*Node, 0, len(nodes))
	for _, node := range nodes {
		if node == nil || !node.Visible || node.Status != "active" {
			continue
		}
		children := PruneVisible(node.Children, allowedMenuIDs)
		if node.Type == "directory" {
			if len(children) == 0 {
				continue
			}
			copyNode := *node
			copyNode.Children = children
			result = append(result, &copyNode)
			continue
		}
		if _, allowed := allowedMenuIDs[node.ID]; !allowed {
			continue
		}
		copyNode := *node
		copyNode.Children = []*Node{}
		result = append(result, &copyNode)
	}
	return result
}

func validPath(value string) bool {
	return pathPattern.MatchString(value) && !strings.Contains(value, "//") && !strings.Contains(value, "..")
}
func validMetadata(value json.RawMessage) bool {
	var object map[string]any
	return json.Valid(value) && json.Unmarshal(value, &object) == nil && object != nil
}
