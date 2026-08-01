// Package consoleapi provides CLI access to the Aimount Console API.
package consoleapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/aimount/aimount-go/internal/httpjson"
)

type Config struct {
	BaseURL string
	Token   string
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

type Principal struct {
	OwnerType string   `json:"ownerType"`
	OwnerID   string   `json:"ownerId"`
	OrgIDs    []string `json:"orgIds"`
}

type Agent struct {
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type InstructionVersion struct {
	ID string `json:"id"`
}

func New(config Config) (*Client, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		return nil, errors.New("consoleapi: base URL is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("consoleapi: invalid base URL")
	}
	if strings.TrimSpace(config.Token) == "" {
		return nil, errors.New("consoleapi: token is required")
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: strings.TrimSpace(config.Token), http: http.DefaultClient}, nil
}

func (c *Client) GetPrincipal(ctx context.Context) (Principal, error) {
	var result Principal
	err := c.do(ctx, http.MethodGet, "/console/v1/me", nil, &result)
	return result, err
}

func (c *Client) ListAgents(ctx context.Context, orgID string) ([]Agent, error) {
	var result []Agent
	path := "/console/v1/orgs/" + url.PathEscape(orgID) + "/agents"
	err := c.do(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

func (c *Client) UpsertAgent(ctx context.Context, orgID, slug, name string) (Agent, error) {
	var result Agent
	path := agentPath(orgID, slug)
	err := c.do(ctx, http.MethodPut, path, struct {
		Name string `json:"name"`
	}{name}, &result)
	if err == nil && (result.Slug == "" || result.Name == "") {
		err = errors.New("consoleapi: malformed agent response")
	}
	return result, err
}

func (c *Client) CreateAgentInstructionVersion(ctx context.Context, orgID, slug, instruction string) (InstructionVersion, error) {
	var result InstructionVersion
	path := agentPath(orgID, slug) + "/instruction/versions"
	err := c.do(ctx, http.MethodPost, path, struct {
		Instruction string `json:"instruction"`
	}{instruction}, &result)
	if err == nil && strings.TrimSpace(result.ID) == "" {
		err = errors.New("consoleapi: malformed instruction version response")
	}
	return result, err
}

func (c *Client) ReleaseAgentInstructionVersion(ctx context.Context, orgID, slug, versionID string) error {
	path := agentPath(orgID, slug) + "/instruction/release"
	return c.do(ctx, http.MethodPost, path, struct {
		VersionID string `json:"versionId"`
	}{versionID}, nil)
}

func agentPath(orgID, slug string) string {
	return "/console/v1/orgs/" + url.PathEscape(orgID) + "/agents/" + url.PathEscape(slug)
}

func (c *Client) do(ctx context.Context, method, path string, body, result any) error {
	requestErr, err := httpjson.Do(ctx, c.http, method, c.baseURL, path, c.token, "", body, result)
	if err != nil {
		return err
	}
	if requestErr != nil {
		if requestErr.Code != "" {
			return fmt.Errorf("consoleapi: %s %s failed: %s", method, path, requestErr.Code)
		}
		return fmt.Errorf("consoleapi: %s %s failed", method, path)
	}
	return nil
}
