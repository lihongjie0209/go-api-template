package application

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/lihongjie0209/microservice-platform-go/stableid"
)

const Namespace = "319595d8-2486-54cb-a522-82b9d6b2d36a"

var (
	ErrInvalid  = errors.New("invalid application")
	ErrNotFound = errors.New("application not found")
	ErrConflict = errors.New("application conflict")
	codePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
)

type Record struct {
	ID            string          `db:"id" json:"id"`
	Code          string          `db:"code" json:"code"`
	Name          string          `db:"name" json:"name"`
	Description   string          `db:"description" json:"description"`
	Icon          string          `db:"icon" json:"icon"`
	HomePath      string          `db:"home_path" json:"home_path"`
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

type Input struct {
	Code, Name, Description, Icon, HomePath, Status string
	SortOrder                                       int64
	Metadata                                        json.RawMessage
}

func Normalize(input Input) Input {
	input.Code = strings.ToLower(strings.TrimSpace(input.Code))
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Icon = strings.TrimSpace(input.Icon)
	input.HomePath = strings.TrimSpace(input.HomePath)
	input.Status = strings.TrimSpace(input.Status)
	if len(input.Metadata) == 0 {
		input.Metadata = json.RawMessage(`{}`)
	}
	return input
}

func Validate(input Input) error {
	input = Normalize(input)
	if !codePattern.MatchString(input.Code) || input.Name == "" || len(input.Name) > 256 ||
		len(input.Description) > 4096 || len(input.Icon) > 256 || len(input.HomePath) > 2048 ||
		(input.Status != "active" && input.Status != "disabled") || input.SortOrder < -1_000_000_000 ||
		input.SortOrder > 1_000_000_000 || len(input.Metadata) > 1<<20 || !validMetadata(input.Metadata) {
		return ErrInvalid
	}
	if input.HomePath != "" && (!strings.HasPrefix(input.HomePath, "/") || strings.Contains(input.HomePath, "//") || strings.Contains(input.HomePath, "..")) {
		return ErrInvalid
	}
	return nil
}

func StableID(code string) (string, error) {
	generator, err := stableid.New(Namespace)
	if err != nil {
		return "", err
	}
	return generator.String("application:" + strings.ToLower(strings.TrimSpace(code)))
}

func validMetadata(value json.RawMessage) bool {
	if !json.Valid(value) {
		return false
	}
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}
