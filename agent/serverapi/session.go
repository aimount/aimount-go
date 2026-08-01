package serverapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"unicode/utf16"

	"github.com/aimount/aimount-go/internal/httpjson"
)

var (
	// ErrInvalidUserID indicates that a user ID is empty or exceeds 256 characters.
	ErrInvalidUserID = errors.New("serverapi: invalid user ID")
	// ErrInvalidSessionID indicates that a session ID is empty or exceeds 256 characters.
	ErrInvalidSessionID = errors.New("serverapi: invalid session ID")
	// ErrInvalidIdempotencyKey indicates that an idempotency key does not match the Agent API contract.
	ErrInvalidIdempotencyKey = errors.New("serverapi: invalid idempotency key")
	// ErrInvalidSessionTitle indicates that a session title exceeds 4096 characters.
	ErrInvalidSessionTitle = errors.New("serverapi: invalid session title")
	// ErrInvalidSessionInstruction indicates that a session instruction exceeds 32000 characters.
	ErrInvalidSessionInstruction = errors.New("serverapi: invalid session instruction")
	// ErrInvalidSessionLabels indicates that session labels do not match the Agent API limits.
	ErrInvalidSessionLabels = errors.New("serverapi: invalid session labels")
)

// CreateSessionParams contains the values used to create a server-managed session.
type CreateSessionParams struct {
	// UserID identifies the client-provided end user.
	UserID string
	// IdempotencyKey identifies this create command across retries.
	IdempotencyKey string
	// Title is omitted when nil. A pointer to an empty string sends an empty title.
	Title *string
	// Labels contains up to 16 client-defined session labels.
	Labels map[string]string
	// Instruction is omitted when nil. A pointer to an empty string sends an empty instruction.
	Instruction *string
}

// ArchiveSessionParams contains the values used to archive a server-managed session.
type ArchiveSessionParams struct {
	// UserID identifies the client-provided end user.
	UserID string
	// SessionID identifies the session to archive.
	SessionID string
	// IdempotencyKey identifies this archive command across retries.
	IdempotencyKey string
}

// RestoreSessionParams contains the values used to restore a server-managed session.
type RestoreSessionParams struct {
	// UserID identifies the client-provided end user.
	UserID string
	// SessionID identifies the session to restore.
	SessionID string
	// IdempotencyKey identifies this restore command across retries.
	IdempotencyKey string
}

// DeleteSessionParams contains the values used to delete a server-managed session.
type DeleteSessionParams struct {
	// UserID identifies the client-provided end user.
	UserID string
	// SessionID identifies the session to delete.
	SessionID string
}

// CreateSessionResult contains identifiers returned after session creation.
type CreateSessionResult struct {
	// SessionID identifies the created session.
	SessionID string
	// CommandID identifies the accepted create command.
	CommandID string
}

// ArchiveSessionResult contains identifiers returned after archiving a session.
type ArchiveSessionResult struct {
	// CommandID identifies the accepted archive command.
	CommandID string
}

// RestoreSessionResult contains identifiers returned after restoring a session.
type RestoreSessionResult struct {
	// CommandID identifies the accepted restore command.
	CommandID string
}

// CreateSession creates a server-managed session for a client-provided end user.
func (c *Client) CreateSession(ctx context.Context, params CreateSessionParams) (CreateSessionResult, error) {
	if c == nil {
		return CreateSessionResult{}, errors.New("serverapi: client is required")
	}
	if !validID(params.UserID) {
		return CreateSessionResult{}, ErrInvalidUserID
	}
	if !validIdempotencyKey(params.IdempotencyKey) {
		return CreateSessionResult{}, ErrInvalidIdempotencyKey
	}
	if params.Title != nil && stringLength(*params.Title) > 4096 {
		return CreateSessionResult{}, ErrInvalidSessionTitle
	}
	if params.Instruction != nil && stringLength(*params.Instruction) > 32000 {
		return CreateSessionResult{}, ErrInvalidSessionInstruction
	}
	if !validSessionLabels(params.Labels) {
		return CreateSessionResult{}, ErrInvalidSessionLabels
	}
	body := struct {
		Title       *string           `json:"title,omitempty"`
		Labels      map[string]string `json:"labels,omitempty"`
		Instruction *string           `json:"instruction,omitempty"`
	}{params.Title, params.Labels, params.Instruction}
	path := c.sessionsPath(params.UserID)
	var wire struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		Ack struct {
			SessionID string `json:"sessionId"`
			CommandID string `json:"commandId"`
			Status    string `json:"status"`
		} `json:"ack"`
	}
	requestErr, err := httpjson.Do(ctx, c.client.http, http.MethodPost, c.client.baseURL, path, c.client.token, params.IdempotencyKey, body, &wire)
	if err != nil {
		var decodeErr httpjson.DecodeError
		if errors.As(err, &decodeErr) {
			return CreateSessionResult{}, protocolError{fmt.Sprintf("serverapi: malformed session response: %v", err)}
		}
		return CreateSessionResult{}, err
	}
	if requestErr != nil {
		return CreateSessionResult{}, Error{Method: http.MethodPost, Path: path, StatusCode: requestErr.StatusCode, Code: requestErr.Code}
	}
	if wire.Session.ID == "" || wire.Ack.SessionID != wire.Session.ID || wire.Ack.CommandID == "" || wire.Ack.Status != "accepted" {
		return CreateSessionResult{}, protocolError{"serverapi: malformed session create response"}
	}
	return CreateSessionResult{SessionID: wire.Session.ID, CommandID: wire.Ack.CommandID}, nil
}

// ArchiveSession archives a server-managed session.
func (c *Client) ArchiveSession(ctx context.Context, params ArchiveSessionParams) (ArchiveSessionResult, error) {
	commandID, err := c.sessionLifecycle(ctx, params.UserID, params.SessionID, params.IdempotencyKey, "archive")
	return ArchiveSessionResult{CommandID: commandID}, err
}

// RestoreSession restores an archived server-managed session.
func (c *Client) RestoreSession(ctx context.Context, params RestoreSessionParams) (RestoreSessionResult, error) {
	commandID, err := c.sessionLifecycle(ctx, params.UserID, params.SessionID, params.IdempotencyKey, "restore")
	return RestoreSessionResult{CommandID: commandID}, err
}

// DeleteSession permanently deletes a server-managed session.
func (c *Client) DeleteSession(ctx context.Context, params DeleteSessionParams) error {
	if c == nil {
		return errors.New("serverapi: client is required")
	}
	if !validID(params.UserID) {
		return ErrInvalidUserID
	}
	if !validID(params.SessionID) {
		return ErrInvalidSessionID
	}
	path := c.sessionsPath(params.UserID) + "/" + url.PathEscape(params.SessionID)
	requestErr, err := httpjson.Do(ctx, c.client.http, http.MethodDelete, c.client.baseURL, path, c.client.token, "", struct{}{}, nil)
	if err != nil {
		return err
	}
	if requestErr != nil {
		return Error{Method: http.MethodDelete, Path: path, StatusCode: requestErr.StatusCode, Code: requestErr.Code}
	}
	return nil
}

func (c *Client) sessionLifecycle(ctx context.Context, userID, sessionID, idempotencyKey, transition string) (string, error) {
	if c == nil {
		return "", errors.New("serverapi: client is required")
	}
	if !validID(userID) {
		return "", ErrInvalidUserID
	}
	if !validID(sessionID) {
		return "", ErrInvalidSessionID
	}
	if !validIdempotencyKey(idempotencyKey) {
		return "", ErrInvalidIdempotencyKey
	}
	path := c.sessionsPath(userID) + "/" + url.PathEscape(sessionID) + "/" + transition
	var ack struct {
		CommandID string `json:"commandId"`
		Status    string `json:"status"`
	}
	requestErr, err := httpjson.Do(ctx, c.client.http, http.MethodPost, c.client.baseURL, path, c.client.token, idempotencyKey, struct{}{}, &ack)
	if err != nil {
		var decodeErr httpjson.DecodeError
		if errors.As(err, &decodeErr) {
			return "", protocolError{fmt.Sprintf("serverapi: malformed session response: %v", err)}
		}
		return "", err
	}
	if requestErr != nil {
		return "", Error{Method: http.MethodPost, Path: path, StatusCode: requestErr.StatusCode, Code: requestErr.Code}
	}
	if ack.CommandID == "" || ack.Status != "accepted" {
		return "", protocolError{"serverapi: malformed session command response"}
	}
	return ack.CommandID, nil
}

func (c *Client) sessionsPath(userID string) string {
	return fmt.Sprintf("/agent/v1/agents/%s/server/users/%s/sessions", url.PathEscape(c.client.agentID), url.PathEscape(userID))
}

func validID(value string) bool { return stringLength(value) >= 1 && stringLength(value) <= 256 }

func validIdempotencyKey(value string) bool {
	if !validID(value) {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '.' && char != '_' && char != ':' && char != '-' {
			return false
		}
	}
	return true
}

func validSessionLabels(labels map[string]string) bool {
	if len(labels) > 16 {
		return false
	}
	for key, value := range labels {
		if stringLength(key) < 1 || stringLength(key) > 64 || stringLength(value) > 256 {
			return false
		}
	}
	return true
}

func stringLength(value string) int { return len(utf16.Encode([]rune(value))) }
