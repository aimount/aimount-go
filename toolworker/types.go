package toolworker

import (
	"context"
	"errors"
	"net/http"
	"time"
)

type ManifestPublishPolicy string

const (
	ManifestPublishNever   ManifestPublishPolicy = "never"
	ManifestPublishOnStart ManifestPublishPolicy = "on_start"

	ManifestConflictReplaceIfTokenMatch ManifestConflictResolutionPolicy = "replace_if_token_match"
	ManifestConflictReplace             ManifestConflictResolutionPolicy = "replace"
)

type ManifestConflictResolutionPolicy string

type PublishOptions struct {
	IfMatchManifestToken     string
	ConflictResolutionPolicy ManifestConflictResolutionPolicy
}

type Logger interface {
	Printf(format string, args ...any)
}

type Config struct {
	BaseURL                string
	AgentID                string
	ToolServiceToken       string
	Namespace              string
	ManifestPublishPolicy  ManifestPublishPolicy
	ManifestPublishOptions PublishOptions
	HTTPClient             *http.Client
	Logger                 Logger
	ErrorMapper            ErrorMapper

	MaxConcurrentCalls int
	ClaimPollInterval  time.Duration
	HeartbeatInterval  time.Duration
	RefreshSkew        time.Duration
}

type PublisherConfig struct {
	BaseURL          string
	AgentID          string
	ToolServiceToken string
	HTTPClient       *http.Client
}

type Definition struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

type Handler func(context.Context, Call) (Outcome, error)

type Call struct {
	Namespace string
	Name      string
	Version   string
	Input     map[string]any
	Subject   Subject
	Deadline  time.Time
}

type Subject struct {
	UserID string `json:"userId"`
}

type RuntimeError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type ToolCallDenial struct {
	Code    string         `json:"code"`
	Details map[string]any `json:"details,omitempty"`
}

type ToolCallCancellation struct {
	Code    string         `json:"code"`
	Details map[string]any `json:"details,omitempty"`
}

type Outcome struct {
	Status       string                `json:"status"`
	Result       any                   `json:"result,omitempty"`
	Error        *RuntimeError         `json:"error,omitempty"`
	Denial       *ToolCallDenial       `json:"denial,omitempty"`
	Cancellation *ToolCallCancellation `json:"cancellation,omitempty"`
}

type ErrorMapper func(error) Outcome

func Succeeded(result any) Outcome {
	return Outcome{Status: "succeeded", Result: result}
}

func Failed(code string, message string, details map[string]any) Outcome {
	return Outcome{Status: "failed", Error: &RuntimeError{Code: code, Message: message, Details: details}}
}

func Denied(code string, details map[string]any) Outcome {
	return Outcome{Status: "denied", Denial: &ToolCallDenial{Code: code, Details: details}}
}

func Cancelled(code string, details map[string]any) Outcome {
	return Outcome{Status: "cancelled", Cancellation: &ToolCallCancellation{Code: code, Details: details}}
}

func defaultErrorMapper(error) Outcome {
	return Failed("tool.internal_error", "tool execution failed", nil)
}

type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Code       string
}

func (e APIError) Error() string {
	if e.Code != "" {
		return e.Method + " " + e.Path + " failed: " + e.Code
	}
	return e.Method + " " + e.Path + " failed"
}

func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var apiErr APIError
	if !errors.As(err, &apiErr) {
		return true
	}
	return apiErr.StatusCode == http.StatusRequestTimeout || apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
}
