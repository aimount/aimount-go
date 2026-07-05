package toolworker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/aimount/aimount-go/tool"
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
	AgentAPIKey            string
	Namespace              string
	ManifestPublishPolicy  ManifestPublishPolicy
	ManifestPublishOptions PublishOptions
	HTTPClient             *http.Client
	Logger                 Logger

	MaxConcurrentCalls int
	ClaimPollInterval  time.Duration
	HeartbeatInterval  time.Duration
	RefreshSkew        time.Duration
}

type PublisherConfig struct {
	BaseURL     string
	AgentID     string
	AgentAPIKey string
	HTTPClient  *http.Client
}

type handler func(context.Context, call) (outcome, error)

type call struct {
	Namespace string
	Name      string
	Version   string
	Input     map[string]any
	inputRaw  json.RawMessage
	Subject   tool.Subject
	Deadline  time.Time
}

type runtimeError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type denial struct {
	Code    string         `json:"code"`
	Details map[string]any `json:"details,omitempty"`
}

type cancellation struct {
	Code    string         `json:"code"`
	Details map[string]any `json:"details,omitempty"`
}

type outcome struct {
	Status       string        `json:"status"`
	Result       any           `json:"result,omitempty"`
	Error        *runtimeError `json:"error,omitempty"`
	Denial       *denial       `json:"denial,omitempty"`
	Cancellation *cancellation `json:"cancellation,omitempty"`
}

func succeeded(result any) outcome {
	return outcome{Status: "succeeded", Result: result}
}

func failed(code string, message string, details map[string]any) outcome {
	return outcome{Status: "failed", Error: &runtimeError{Code: code, Message: message, Details: details}}
}

func internalFailure() outcome {
	return failed(tool.InternalErrorCode, tool.InternalErrorMessage, nil)
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
