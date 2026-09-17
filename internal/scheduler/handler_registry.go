package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
)

var ErrHandlerNotRegistered = errors.New("scheduled job handler is not registered")

type Handler func(context.Context, json.RawMessage) error

type HandlerDefinition struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	handler     Handler
}

type HandlerRegistry struct{ handlers map[string]HandlerDefinition }

func NewHandlerRegistry(logger *slog.Logger) *HandlerRegistry {
	definition := HandlerDefinition{Key: "system.sample", Name: "系统示例任务", Description: "记录一条带链路上下文的示例日志", handler: func(ctx context.Context, _ json.RawMessage) error {
		logger.InfoContext(ctx, "sample scheduled job executed", "handler", "system.sample")
		return nil
	}}
	return &HandlerRegistry{handlers: map[string]HandlerDefinition{definition.Key: definition}}
}

func (r *HandlerRegistry) Resolve(key string) (Handler, error) {
	definition, ok := r.handlers[strings.TrimSpace(key)]
	if !ok {
		return nil, ErrHandlerNotRegistered
	}
	return definition.handler, nil
}

func (r *HandlerRegistry) Definitions() []HandlerDefinition {
	result := make([]HandlerDefinition, 0, len(r.handlers))
	for _, definition := range r.handlers {
		definition.handler = nil
		result = append(result, definition)
	}
	slices.SortFunc(result, func(left, right HandlerDefinition) int { return strings.Compare(left.Key, right.Key) })
	return result
}
