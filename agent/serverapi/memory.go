package serverapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aimount/aimount-go/internal/httpjson"
)

// MemoryActor identifies a manager or author, not an individual user's identity.
type MemoryActor string

const (
	MemoryActorUser    MemoryActor = "user"
	MemoryActorAgent   MemoryActor = "agent"
	MemoryActorService MemoryActor = "service"
	// MaxMemoryVersion is the largest positive JSON-safe integer version.
	MaxMemoryVersion int64 = 9007199254740991
)

var (
	ErrInvalidMemoryID        = errors.New("serverapi: invalid memory ID")
	ErrInvalidMemoryText      = errors.New("serverapi: memory text must be nonblank UTF-8 without NUL and at most 2048 bytes")
	ErrInvalidMemoryVersion   = errors.New("serverapi: memory version must be between 1 and 9007199254740991")
	ErrInvalidMemoryManagedBy = errors.New("serverapi: memory managedBy must be user, agent or service")
)

// AgentMemoryBlock is current memory shared by all sessions of one agent and end user.
type AgentMemoryBlock struct {
	ID        string      `json:"id"`
	Text      string      `json:"text"`
	Version   int64       `json:"version"`
	ManagedBy MemoryActor `json:"managedBy"`
	CreatedBy MemoryActor `json:"createdBy"`
	UpdatedBy MemoryActor `json:"updatedBy"`
	CreatedAt time.Time   `json:"createdAt"`
	UpdatedAt time.Time   `json:"updatedAt"`
}

type ListMemoryParams struct {
	UserID string
}

type CreateMemoryParams struct {
	UserID string
	Text   string
	// ManagedBy defaults to service when omitted.
	ManagedBy MemoryActor
	// IdempotencyKey is generated when empty. Reuse the returned key and exact
	// creation content for retries within 24 hours of successful creation.
	IdempotencyKey string
}

type UpdateMemoryParams struct {
	UserID   string
	MemoryID string
	// Version is the observed version, not the desired next version.
	Version int64
	Text    string
}

type DeleteMemoryParams struct {
	UserID   string
	MemoryID string
	// Version is the observed version of the block to delete.
	Version int64
}

type ListMemoryResult struct {
	Blocks []AgentMemoryBlock `json:"blocks"`
}

type CreateMemoryResult struct {
	Operation string           `json:"operation"`
	Block     AgentMemoryBlock `json:"block"`
	// IdempotencyKey is also returned on failure, after generation/validation.
	IdempotencyKey string `json:"-"`
}

type UpdateMemoryResult struct {
	Operation string           `json:"operation"`
	Block     AgentMemoryBlock `json:"block"`
}

type DeleteMemoryResult struct {
	Operation string `json:"operation"`
	ID        string `json:"id"`
	// Version is the last deleted version, not a new tombstone version.
	Version int64 `json:"version"`
}

// ListMemory reads the authoritative current collection. It does not infer
// which writer caused a change when reconciling an uncertain mutation.
func (c *Client) ListMemory(ctx context.Context, params ListMemoryParams) (ListMemoryResult, error) {
	var result ListMemoryResult
	if err := c.memoryRequest(ctx, http.MethodGet, params.UserID, "", 0, "", nil, &result); err != nil {
		return ListMemoryResult{}, err
	}
	return result, nil
}

// CreateMemory creates one block. It makes one request and exposes its key on
// both the result and Error so an application can replay a lost response.
func (c *Client) CreateMemory(ctx context.Context, params CreateMemoryParams) (CreateMemoryResult, error) {
	if !validMemoryText(params.Text) {
		return CreateMemoryResult{}, ErrInvalidMemoryText
	}
	if params.ManagedBy != "" && !validMemoryActor(params.ManagedBy) {
		return CreateMemoryResult{}, ErrInvalidMemoryManagedBy
	}
	key := params.IdempotencyKey
	if key == "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return CreateMemoryResult{}, fmt.Errorf("serverapi: generate memory idempotency key: %w", err)
		}
		key = "aimount-go-" + hex.EncodeToString(nonce[:])
	} else if !validIdempotencyKey(key) {
		return CreateMemoryResult{}, ErrInvalidIdempotencyKey
	}
	body := struct {
		Text      string      `json:"text"`
		ManagedBy MemoryActor `json:"managedBy,omitempty"`
	}{params.Text, params.ManagedBy}
	result := CreateMemoryResult{IdempotencyKey: key}
	if err := c.memoryRequest(ctx, http.MethodPost, params.UserID, "", 0, key, body, &result); err != nil {
		return CreateMemoryResult{IdempotencyKey: key}, err
	}
	return result, nil
}

// UpdateMemory replaces text at the observed version. It never retries or
// substitutes a newer version; reconcile Error.OutcomeUnknown with ListMemory.
func (c *Client) UpdateMemory(ctx context.Context, params UpdateMemoryParams) (UpdateMemoryResult, error) {
	if !validMemoryText(params.Text) {
		return UpdateMemoryResult{}, ErrInvalidMemoryText
	}
	body := struct {
		Text string `json:"text"`
	}{params.Text}
	var result UpdateMemoryResult
	if err := c.memoryRequest(ctx, http.MethodPatch, params.UserID, params.MemoryID, params.Version, "", body, &result); err != nil {
		return UpdateMemoryResult{}, err
	}
	return result, nil
}

// DeleteMemory removes a block at the observed version without a request body
// or automatic retry. A successful result reports the last deleted version.
func (c *Client) DeleteMemory(ctx context.Context, params DeleteMemoryParams) (DeleteMemoryResult, error) {
	var result DeleteMemoryResult
	if err := c.memoryRequest(ctx, http.MethodDelete, params.UserID, params.MemoryID, params.Version, "", nil, &result); err != nil {
		return DeleteMemoryResult{}, err
	}
	return result, nil
}

func (c *Client) memoryRequest(ctx context.Context, method, userID, memoryID string, version int64, key string, body, result any) error {
	if c == nil {
		return errors.New("serverapi: client is required")
	}
	if !validID(userID) {
		return ErrInvalidUserID
	}
	path := fmt.Sprintf("/agent/v1/agents/%s/server/users/%s/memory", url.PathEscape(c.client.agentID), url.PathEscape(userID))
	conditional := method == http.MethodPatch || method == http.MethodDelete
	if conditional {
		if !validID(memoryID) {
			return ErrInvalidMemoryID
		}
		if version < 1 || version > MaxMemoryVersion {
			return ErrInvalidMemoryVersion
		}
		path += "/" + url.PathEscape(memoryID)
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.client.baseURL, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.client.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if conditional {
		req.Header.Set("If-Match", `"`+strconv.FormatInt(version, 10)+`"`)
	}
	write := method != http.MethodGet
	httpClient := *c.client.http
	if write {
		// Redirects can replay writes (307/308) or disguise them as reads (301/302/303).
		// Clear GetBody as well so net/http cannot replay a create on a reused connection.
		httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		req.GetBody = nil
	}
	apiErr := Error{Method: method, Path: path, IdempotencyKey: key}
	if err := ctx.Err(); err != nil {
		apiErr.cause = err
		return apiErr
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		apiErr.OutcomeUnknown, apiErr.cause = write, err
		return apiErr
	}
	defer resp.Body.Close()
	apiErr.StatusCode = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A valid 2-KiB text can expand to 12 KiB when JSON-escaped in conflict details.
		var envelope struct {
			Type    string          `json:"type"`
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
			Error   *struct {
				Type    string          `json:"type"`
				Code    string          `json:"code"`
				Message string          `json:"message"`
				Details json.RawMessage `json:"details"`
			} `json:"error"`
		}
		decodeErr := httpjson.Decode(io.LimitReader(resp.Body, 1<<20), &envelope)
		if decodeErr == nil {
			apiErr.Type, apiErr.Code, apiErr.Message, apiErr.Details = envelope.Type, envelope.Code, envelope.Message, envelope.Details
			if envelope.Error != nil {
				apiErr.Type, apiErr.Code, apiErr.Message, apiErr.Details = envelope.Error.Type, envelope.Error.Code, envelope.Error.Message, envelope.Error.Details
			}
		}
		// Gateway/server failures and timeouts cannot establish that a write was rejected.
		apiErr.OutcomeUnknown = write && (resp.StatusCode < 400 || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode >= 500)
		return apiErr
	}
	// Check wire UTF-8 before encoding/json can replace invalid bytes in strings.
	var raw json.RawMessage
	err = httpjson.Decode(resp.Body, &raw)
	if err == nil && !utf8.Valid(raw) {
		err = errors.New("invalid UTF-8")
	}
	if err == nil {
		err = json.Unmarshal(raw, result)
	}
	if err != nil {
		apiErr.OutcomeUnknown = write
		apiErr.cause = protocolError{fmt.Sprintf("serverapi: malformed memory response: %v", err)}
		return apiErr
	}
	if !validMemoryResult(result) {
		apiErr.OutcomeUnknown = write
		apiErr.cause = protocolError{"serverapi: malformed memory response"}
		return apiErr
	}
	return nil
}

func validMemoryActor(actor MemoryActor) bool {
	return actor == MemoryActorUser || actor == MemoryActorAgent || actor == MemoryActorService
}

func validMemoryText(text string) bool {
	// Match Cloud's ECMAScript trim: includes U+FEFF, but not Go's U+0085.
	const whitespace = "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"
	return utf8.ValidString(text) && !strings.ContainsRune(text, '\x00') && strings.Trim(text, whitespace) != "" && len(text) <= 2048
}

func validMemoryBlock(block *AgentMemoryBlock) bool {
	if !validID(block.ID) || !validMemoryText(block.Text) || block.Version < 1 || block.Version > MaxMemoryVersion ||
		!validMemoryActor(block.ManagedBy) || !validMemoryActor(block.CreatedBy) || !validMemoryActor(block.UpdatedBy) ||
		block.CreatedAt.IsZero() || block.UpdatedAt.IsZero() {
		return false
	}
	block.CreatedAt, block.UpdatedAt = block.CreatedAt.UTC(), block.UpdatedAt.UTC()
	return true
}

func validMemoryResult(result any) bool {
	switch result := result.(type) {
	case *ListMemoryResult:
		if result.Blocks == nil {
			return false
		}
		for i := range result.Blocks {
			if !validMemoryBlock(&result.Blocks[i]) {
				return false
			}
		}
		return true
	case *CreateMemoryResult:
		return result.Operation == "create" && validMemoryBlock(&result.Block)
	case *UpdateMemoryResult:
		return result.Operation == "update" && validMemoryBlock(&result.Block)
	case *DeleteMemoryResult:
		return result.Operation == "delete" && validID(result.ID) && result.Version >= 1 && result.Version <= MaxMemoryVersion
	default:
		return false
	}
}
