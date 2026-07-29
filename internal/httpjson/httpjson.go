package httpjson

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Error struct {
	StatusCode int
	Code       string
}

type DecodeError struct{ Err error }

func (e DecodeError) Error() string { return e.Err.Error() }
func (e DecodeError) Unwrap() error { return e.Err }

func Do(ctx context.Context, client *http.Client, method, baseURL, path, token, idempotencyKey string, body, result any) (*Error, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+token)
	if idempotencyKey != "" {
		req.Header.Set("idempotency-key", idempotencyKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &Error{StatusCode: resp.StatusCode, Code: ErrorCode(responseBody)}, nil
	}
	if result == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, nil
	}
	if err := Decode(resp.Body, result); err != nil {
		return nil, DecodeError{Err: err}
	}
	return nil, nil
}

func Decode(reader io.Reader, result any) error {
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(result); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON response")
		}
		return fmt.Errorf("trailing JSON response: %w", err)
	}
	return nil
}

func ErrorCode(body []byte) string {
	var envelope struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
		Code string `json:"code"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	if envelope.Error != nil {
		return envelope.Error.Code
	}
	return envelope.Code
}

func RetryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}
