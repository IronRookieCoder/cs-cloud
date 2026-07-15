package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"time"

	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
)

// Client is a REST client for the multica backend.
type Client struct {
	baseURL       string
	tokenProvider func() (*provider.Credentials, error)
	http          *http.Client
}

// NewClient creates a new multica REST client.
func NewClient(baseURL string, tp func() (*provider.Credentials, error)) *Client {
	return &Client{
		baseURL:       baseURL,
		tokenProvider: tp,
		http:          &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) token() (string, error) {
	cred, err := c.tokenProvider()
	if err != nil {
		return "", err
	}
	if cred == nil || cred.AccessToken == "" {
		return "", fmt.Errorf("no access token")
	}
	return cred.AccessToken, nil
}

func (c *Client) request(ctx context.Context, method, path string, body, out any) error {
	token, err := c.token()
	if err != nil {
		return err
	}

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(workflow.HeaderClientPlatform, "cs-cloud")
	req.Header.Set(workflow.HeaderClientVersion, "dev")
	req.Header.Set(workflow.HeaderClientOS, runtime.GOOS)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s returned %d: %s", method, path, resp.StatusCode, string(b))
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// GetWorkspaces fetches all workspaces from multica.
func (c *Client) GetWorkspaces() ([]workflow.Workspace, error) {
	var out []workflow.Workspace
	err := c.request(context.Background(), http.MethodGet, workflow.MulticaWorkspacesEndpoint, nil, &out)
	return out, err
}

// StartTask marks a task as started.
func (c *Client) StartTask(taskID string) error {
	path := fmt.Sprintf(workflow.MulticaTaskStartEndpoint, taskID)
	return c.request(context.Background(), http.MethodPost, path, nil, nil)
}

// CompleteTask marks a task as complete with the given result.
func (c *Client) CompleteTask(taskID string, result any) error {
	path := fmt.Sprintf(workflow.MulticaTaskCompleteEndpoint, taskID)
	return c.request(context.Background(), http.MethodPost, path, map[string]any{"result": result}, nil)
}

// FailTask marks a task as failed with the given reason.
func (c *Client) FailTask(taskID string, reason string) error {
	path := fmt.Sprintf(workflow.MulticaTaskFailEndpoint, taskID)
	return c.request(context.Background(), http.MethodPost, path, map[string]any{"error": reason}, nil)
}

// PostTaskMessages uploads task output messages.
func (c *Client) PostTaskMessages(taskID string, messages string) error {
	path := fmt.Sprintf(workflow.MulticaTaskMessagesEndpoint, taskID)
	return c.request(context.Background(), http.MethodPost, path, map[string]any{"messages": messages}, nil)
}
