package lmstudio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Client struct {
	baseURL     string
	restBaseURL string
	httpClient  HTTPClient
}

func NewClient(baseURL string, httpClient HTTPClient) *Client {
	if httpClient == nil {
		// No client-level timeout: streaming responses can be arbitrarily long.
		// Individual request timeouts are handled via the request context.
		httpClient = &http.Client{}
	}

	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		restBaseURL: restBaseURL(strings.TrimRight(baseURL, "/")),
		httpClient:  httpClient,
	}
}

func (c *Client) PostJSON(ctx context.Context, path string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal upstream payload: %w", err)
	}

	return c.Post(ctx, path, bytes.NewReader(body), "application/json", "application/json")
}

func (c *Client) Post(ctx context.Context, path string, body io.Reader, contentType, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path), body)
	if err != nil {
		return nil, fmt.Errorf("create upstream request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) Get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return nil, fmt.Errorf("create upstream request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) url(path string) string {
	if strings.HasPrefix(path, "/api/") {
		return c.restBaseURL + path
	}
	return c.baseURL + path
}

func restBaseURL(baseURL string) string {
	if strings.HasSuffix(baseURL, "/v1") {
		return strings.TrimSuffix(baseURL, "/v1")
	}
	return baseURL
}
