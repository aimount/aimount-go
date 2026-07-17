package toolworker

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/aimount/aimount-go/tool"
)

const (
	ManifestConflictReplaceIfTokenMatch ManifestConflictResolutionPolicy = "replace_if_token_match"
	ManifestConflictReplace             ManifestConflictResolutionPolicy = "replace"
)

var errMalformedProtocol = errors.New("toolworker: malformed protocol response")

type protocolError struct{ message string }

func (e protocolError) Error() string { return e.message }
func (e protocolError) Unwrap() error { return errMalformedProtocol }

type ManifestConflictResolutionPolicy string

type PublishOptions struct {
	IfMatchManifestToken     string
	ConflictResolutionPolicy ManifestConflictResolutionPolicy
}

type ClientConfig struct {
	BaseURL     string
	AgentID     string
	AgentAPIKey string
	HTTPClient  *http.Client
}

type WorkerConfig struct {
	Client             ClientConfig
	Logger             *slog.Logger
	MaxConcurrentCalls int
	ClaimPollInterval  time.Duration
	HeartbeatInterval  time.Duration
	RefreshSkew        time.Duration
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
	Status       string          `json:"status"`
	Result       json.RawMessage `json:"result,omitempty"`
	Error        *runtimeError   `json:"error,omitempty"`
	Denial       *denial         `json:"denial,omitempty"`
	Cancellation *cancellation   `json:"cancellation,omitempty"`
}

func succeeded(result json.RawMessage) outcome {
	return outcome{Status: "succeeded", Result: result}
}

func failed(code string, message string, details map[string]any) outcome {
	return outcome{Status: "failed", Error: &runtimeError{Code: code, Message: message, Details: details}}
}

func internalFailure() outcome {
	return failed(tool.UnknownErrorCode, tool.UnknownErrorMessage, nil)
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
	if errors.Is(err, errMalformedProtocol) {
		return false
	}
	var apiErr APIError
	if !errors.As(err, &apiErr) {
		return true
	}
	return apiErr.StatusCode == http.StatusRequestTimeout || apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
}
