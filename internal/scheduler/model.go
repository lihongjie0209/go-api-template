package scheduler

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

var (
	ErrInvalidDefinition  = errors.New("invalid scheduled job definition")
	ErrDefinitionNotFound = errors.New("scheduled job definition not found")
	ErrDefinitionConflict = errors.New("scheduled job definition conflict")
	definitionKeyPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
	handlerKeyPattern     = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*$`)
	definitionParser      = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
)

type Definition struct {
	ID             string          `db:"id" json:"id"`
	Code           string          `db:"code" json:"code"`
	Name           string          `db:"name" json:"name"`
	Description    string          `db:"description" json:"description"`
	CronSpec       string          `db:"cron_spec" json:"cron_spec"`
	Timezone       string          `db:"timezone" json:"timezone"`
	Handler        string          `db:"handler" json:"handler"`
	TimeoutSeconds int64           `db:"timeout_seconds" json:"timeout_seconds"`
	LockTTLSeconds int64           `db:"lock_ttl_seconds" json:"lock_ttl_seconds"`
	Status         string          `db:"status" json:"status"`
	Payload        json.RawMessage `db:"payload" json:"payload" swaggertype:"object"`
	CreatedAt      time.Time       `db:"created_at" json:"created_at"`
	CreatedBy      string          `db:"created_by" json:"created_by"`
	CreatedByName  string          `db:"-" json:"created_by_name"`
	UpdatedAt      time.Time       `db:"updated_at" json:"updated_at"`
	UpdatedBy      string          `db:"updated_by" json:"updated_by"`
	UpdatedByName  string          `db:"-" json:"updated_by_name"`
	Version        int64           `db:"version" json:"version"`
}

type DefinitionInput struct {
	Code           string
	Name           string
	Description    string
	CronSpec       string
	Timezone       string
	Handler        string
	TimeoutSeconds int64
	LockTTLSeconds int64
	Status         string
	Payload        json.RawMessage
}

type Run struct {
	ID             string    `db:"id" json:"id"`
	ScheduledJobID string    `db:"scheduled_job_id" json:"scheduled_job_id"`
	JobCode        string    `db:"job_code" json:"job_code"`
	JobName        string    `db:"job_name" json:"job_name"`
	Handler        string    `db:"handler" json:"handler"`
	TriggerSource  string    `db:"trigger_source" json:"trigger_source"`
	Status         string    `db:"status" json:"status"`
	RequestID      string    `db:"request_id" json:"request_id"`
	TraceID        string    `db:"trace_id" json:"trace_id"`
	StartedAt      time.Time `db:"started_at" json:"started_at"`
	FinishedAt     time.Time `db:"finished_at" json:"finished_at"`
	DurationMS     int64     `db:"duration_ms" json:"duration_ms"`
	ErrorMessage   string    `db:"error_message" json:"error_message"`
	CreatedAt      time.Time `db:"created_at" json:"created_at"`
	CreatedBy      string    `db:"created_by" json:"created_by"`
	CreatedByName  string    `db:"-" json:"created_by_name"`
	UpdatedAt      time.Time `db:"updated_at" json:"updated_at"`
	UpdatedBy      string    `db:"updated_by" json:"updated_by"`
	UpdatedByName  string    `db:"-" json:"updated_by_name"`
	Version        int64     `db:"version" json:"version"`
}

func normalizeDefinition(input DefinitionInput) DefinitionInput {
	input.Code = strings.ToLower(strings.TrimSpace(input.Code))
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.CronSpec = strings.Join(strings.Fields(input.CronSpec), " ")
	input.Timezone = strings.TrimSpace(input.Timezone)
	input.Handler = strings.ToLower(strings.TrimSpace(input.Handler))
	input.Status = strings.TrimSpace(input.Status)
	if len(input.Payload) == 0 {
		input.Payload = json.RawMessage(`{}`)
	}
	return input
}

func validateDefinition(input DefinitionInput) error {
	input = normalizeDefinition(input)
	if !definitionKeyPattern.MatchString(input.Code) || input.Name == "" || len(input.Name) > 256 || len(input.Description) > 4096 ||
		len(input.CronSpec) > 256 || len(input.Timezone) > 100 || !handlerKeyPattern.MatchString(input.Handler) || len(input.Handler) > 128 ||
		input.TimeoutSeconds < 1 || input.TimeoutSeconds > 3600 || input.LockTTLSeconds < 1 || input.LockTTLSeconds > 86400 ||
		(input.Status != "active" && input.Status != "disabled") || len(input.Payload) > 1<<20 || !validDefinitionPayload(input.Payload) {
		return ErrInvalidDefinition
	}
	if _, err := definitionParser.Parse(input.CronSpec); err != nil {
		return ErrInvalidDefinition
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return ErrInvalidDefinition
	}
	return nil
}

func validDefinitionPayload(value json.RawMessage) bool {
	if !json.Valid(value) {
		return false
	}
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}
