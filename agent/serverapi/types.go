package serverapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

const (
	unknownToolErrorCode    = "unknown"
	unknownToolErrorMessage = "Tool execution failed"

	ManifestConflictReplaceIfTokenMatch ManifestConflictResolutionPolicy = "replace_if_token_match"
	ManifestConflictReplace             ManifestConflictResolutionPolicy = "replace"
)

var errMalformedProtocol = errors.New("serverapi: malformed protocol response")

type protocolError struct{ message string }

func (e protocolError) Error() string { return e.message }
func (e protocolError) Unwrap() error { return errMalformedProtocol }

type ManifestConflictResolutionPolicy string

type PublishToolManifestOptions struct {
	IfMatchManifestToken     string
	ConflictResolutionPolicy ManifestConflictResolutionPolicy
}

type ToolWorkerOptions struct {
	Logger             *slog.Logger
	MaxConcurrentCalls int
	ClaimPollInterval  time.Duration
	HeartbeatInterval  time.Duration
	RefreshSkew        time.Duration
}

type agentToolError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type outcome struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *agentToolError `json:"error,omitempty"`
}

func succeeded(result json.RawMessage) outcome {
	return outcome{Status: "succeeded", Result: result}
}

func failed(code string, message string, details map[string]any) outcome {
	return outcome{Status: "failed", Error: &agentToolError{Code: code, Message: message, Details: details}}
}

func internalFailure() outcome {
	return failed(unknownToolErrorCode, unknownToolErrorMessage, nil)
}
