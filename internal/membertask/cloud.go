package membertask

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"cs-cloud/internal/provider"
)

const (
	cloudRequestTimeout  = 15 * time.Second
	cloudReadAttempts    = 3
	maxErrorBodyBytes    = 64 << 10
	maxResponseBodyBytes = 16 << 20
)

type RemoteTask struct {
	Ref              TaskKey `json:"ref,omitempty"`
	Title            string  `json:"title,omitempty"`
	DisplayName      string  `json:"display_name,omitempty"`
	IssueTitle       string  `json:"issue_title,omitempty"`
	IssueID          string  `json:"issue_id,omitempty"`
	IssueNumber      int32   `json:"issue_number,omitempty"`
	IssueIdentifier  string  `json:"issue_identifier,omitempty"`
	IssueDescription string  `json:"issue_description,omitempty"`
	WorkspaceSlug    string  `json:"workspace_slug,omitempty"`
	TaskKind         string  `json:"task_kind,omitempty"`
	NodeName         string  `json:"node_name,omitempty"`
	CloudInstanceID  string  `json:"cloud_instance_id"`
	WorkspaceID      string  `json:"workspace_id"`
	NodeRunID        string  `json:"node_run_id"`
	Role             Role    `json:"role"`
	Attempt          int     `json:"attempt"`
	TaskVersion      int64   `json:"task_version"`
	ContextVersion   int64   `json:"context_version"`
	CloudStatus      string  `json:"cloud_status"`
}

func (t RemoteTask) Key() TaskKey {
	if t.Ref.NodeRunID != "" {
		return t.Ref
	}
	return TaskKey{CloudInstanceID: t.CloudInstanceID, WorkspaceID: t.WorkspaceID, NodeRunID: t.NodeRunID, Role: t.Role}
}

type MaterialSource struct {
	ID            string             `json:"id,omitempty"`
	Identity      string             `json:"identity"`
	Kind          string             `json:"kind"`
	Name          string             `json:"name,omitempty"`
	Title         string             `json:"title,omitempty"`
	SourceURL     string             `json:"source_url,omitempty"`
	Version       string             `json:"version,omitempty"`
	ReadOnly      bool               `json:"read_only,omitempty"`
	SourceVersion string             `json:"source_version"`
	RelativePath  string             `json:"relative_path"`
	Role          MaterialRole       `json:"role"`
	SHA256        string             `json:"sha256,omitempty"`
	Content       string             `json:"content,omitempty"`
	Source        *DeliverableSource `json:"source,omitempty"`
	Repository    *RepositoryContext `json:"repository,omitempty"`
}

type RemoteDeliverable struct {
	ID               string             `json:"id"`
	Title            string             `json:"title"`
	Description      string             `json:"description,omitempty"`
	Required         bool               `json:"required"`
	Purpose          string             `json:"purpose,omitempty"`
	Missing          bool               `json:"missing"`
	Content          string             `json:"content,omitempty"`
	ContentAvailable bool               `json:"-"`
	URL              string             `json:"url,omitempty"`
	Version          string             `json:"version,omitempty"`
	SHA256           string             `json:"sha256,omitempty"`
	Source           *DeliverableSource `json:"source,omitempty"`
}

type DeliverableSource struct {
	Provider   string `json:"provider,omitempty"`
	Repository string `json:"repository,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Path       string `json:"path,omitempty"`
}

func (d *RemoteDeliverable) UnmarshalJSON(data []byte) error {
	type deliverableAlias RemoteDeliverable
	var value deliverableAlias
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*d = RemoteDeliverable(value)
	_, d.ContentAvailable = fields["content"]
	return nil
}

type RepositoryContext struct {
	Provider       string   `json:"provider,omitempty"`
	Identity       string   `json:"identity"`
	SourceURL      string   `json:"source_url,omitempty"`
	CloneURL       string   `json:"clone_url"`
	BaseRef        string   `json:"base_ref"`
	BaseSHA        string   `json:"base_sha"`
	BeforeSHA      string   `json:"before_sha"`
	TargetRef      string   `json:"target_ref"`
	ProtectedPaths []string `json:"protected_paths,omitempty"`
	OutputPaths    []string `json:"output_paths,omitempty"`
	HeadSHA        string   `json:"head_sha,omitempty"`
	PrepareAllowed bool     `json:"prepare_allowed,omitempty"`
}

type RemoteTaskContext struct {
	RemoteTask
	Goal                 string              `json:"goal,omitempty"`
	Objective            string              `json:"objective,omitempty"`
	AcceptanceCriteria   []string            `json:"acceptance_criteria,omitempty"`
	RequiredDeliverables []RemoteDeliverable `json:"required_deliverables,omitempty"`
	OptionalDeliverables []RemoteDeliverable `json:"optional_deliverables,omitempty"`
	PredecessorResults   []RemoteDeliverable `json:"predecessor_results,omitempty"`
	ReworkReason         string              `json:"rework_reason,omitempty"`
	PreviousResults      []RemoteDeliverable `json:"previous_results,omitempty"`
	PrepareAllowed       bool                `json:"prepare_allowed"`
	Providers            []string            `json:"providers,omitempty"`
	Materials            []MaterialSource    `json:"materials,omitempty"`
	Repositories         []RepositoryContext `json:"repositories,omitempty"`
	MaterialDigest       string              `json:"material_digest,omitempty"`
}

type apiErrorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`
}

type CloudClient struct {
	baseURL string
	token   func() (*provider.Credentials, error)
	http    *http.Client
}

func NewCloudClient(baseURL string, token func() (*provider.Credentials, error)) *CloudClient {
	return &CloudClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: cloudRequestTimeout},
	}
}

func (c *CloudClient) List(ctx context.Context) ([]RemoteTask, error) {
	var raw json.RawMessage
	if err := c.doJSON(ctx, nil, http.MethodGet, "/api/member/tasks", nil, &raw); err != nil {
		return nil, err
	}
	var out struct {
		Tasks []RemoteTask `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		if err := json.Unmarshal(raw, &out.Tasks); err != nil {
			return nil, newTaskError("invalid_cloud_response", "cloud returned an invalid task list", nil)
		}
	}
	if out.Tasks == nil {
		out.Tasks = []RemoteTask{}
	}
	for _, task := range out.Tasks {
		if _, err := ParseTaskKey(task.Key().String()); err != nil {
			return nil, newTaskError("invalid_cloud_response", "cloud returned an invalid task identity", nil)
		}
	}
	return out.Tasks, nil
}

func (c *CloudClient) GetContext(ctx context.Context, key TaskKey) (RemoteTaskContext, error) {
	endpoint := "/api/member/tasks/" + url.PathEscape(key.NodeRunID) + "/" + url.PathEscape(string(key.Role)) + "/context"
	var out RemoteTaskContext
	if err := c.doJSON(ctx, &key, http.MethodGet, endpoint, nil, &out); err != nil {
		return RemoteTaskContext{}, err
	}
	if out.Key() != key {
		return RemoteTaskContext{}, newTaskError("invalid_cloud_response", "cloud returned a mismatched task identity", nil)
	}
	if out.Goal == "" {
		out.Goal = out.Objective
	}
	if len(out.Providers) == 0 {
		seen := map[string]struct{}{}
		for _, repository := range out.Repositories {
			if repository.Provider == "" {
				continue
			}
			if _, ok := seen[repository.Provider]; !ok {
				seen[repository.Provider] = struct{}{}
				out.Providers = append(out.Providers, repository.Provider)
			}
		}
		sort.Strings(out.Providers)
	}
	return out, nil
}

func (c *CloudClient) PreviewSubmit(ctx context.Context, key TaskKey, request SubmitPreviewRequest) (Preview, error) {
	endpoint := "/api/member/tasks/" + url.PathEscape(key.NodeRunID) + "/" + url.PathEscape(string(key.Role)) + "/submit/preview"
	var out Preview
	err := c.doJSON(ctx, &key, http.MethodPost, endpoint, request, &out)
	return out, err
}

func (c *CloudClient) PreviewReview(ctx context.Context, key TaskKey, request ReviewPreviewRequest) (Preview, error) {
	endpoint := "/api/member/tasks/" + url.PathEscape(key.NodeRunID) + "/" + url.PathEscape(string(key.Role)) + "/review/preview"
	var out Preview
	err := c.doJSON(ctx, &key, http.MethodPost, endpoint, request, &out)
	return out, err
}

func (c *CloudClient) ConfirmOperation(ctx context.Context, key TaskKey, previewID string) (Operation, error) {
	var out Operation
	endpoint := "/api/member/task-operations/" + url.PathEscape(previewID) + "/confirm"
	err := c.doJSON(ctx, &key, http.MethodPost, endpoint, nil, &out)
	if err == nil || !retryableReadError(err) {
		return out, err
	}
	out = Operation{}
	if err := c.doJSON(ctx, &key, http.MethodPost, endpoint, nil, &out); err != nil {
		return Operation{}, err
	}
	if out.Status == OperationCompleted {
		out.Outcome = OutcomeRecovered
	}
	return out, nil
}

func (c *CloudClient) GetOperation(ctx context.Context, key TaskKey, operationID string) (Operation, error) {
	var out Operation
	err := c.doJSON(ctx, &key, http.MethodGet, "/api/member/task-operations/"+url.PathEscape(operationID), nil, &out)
	return out, err
}

func (c *CloudClient) ReportOperation(ctx context.Context, key TaskKey, operationID string, report StepReport) (Operation, error) {
	var out Operation
	err := c.doJSON(ctx, &key, http.MethodPost, "/api/member/task-operations/"+url.PathEscape(operationID)+"/report", report, &out)
	return out, err
}

func (c *CloudClient) doJSON(ctx context.Context, key *TaskKey, method, endpoint string, body any, out any) error {
	if c == nil || c.baseURL == "" {
		return newTaskError("cloud_unavailable", "cloud base URL is not configured", nil)
	}
	base, err := url.Parse(c.baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return newTaskError("cloud_unavailable", "cloud base URL is invalid", err)
	}
	base.Path = path.Join(base.Path, endpoint)
	attempts := 1
	if method == http.MethodGet {
		attempts = cloudReadAttempts
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		err = c.doJSONOnce(ctx, key, method, base.String(), body, out)
		if err == nil || !retryableReadError(err) || attempt == attempts {
			return err
		}
		select {
		case <-ctx.Done():
			return newTaskError("cloud_unavailable", "cloud request canceled", ctx.Err())
		case <-time.After(time.Duration(attempt) * 25 * time.Millisecond):
		}
	}
	return err
}

func (c *CloudClient) doJSONOnce(ctx context.Context, key *TaskKey, method, endpoint string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return newTaskError("invalid_request", "cannot encode cloud request", err)
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return newTaskError("cloud_unavailable", "cannot build cloud request", err)
	}
	credentials, err := c.token()
	if err != nil || credentials == nil || strings.TrimSpace(credentials.AccessToken) == "" {
		return newTaskError("authentication_required", "cloud credentials are unavailable", err)
	}
	req.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	req.Header.Set("Accept", "application/json")
	if key != nil {
		req.Header.Set("X-Workspace-ID", key.WorkspaceID)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return newTaskError("cloud_unavailable", "cloud request failed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes+1))
		var envelope apiErrorEnvelope
		if len(payload) <= maxErrorBodyBytes && json.Unmarshal(payload, &envelope) == nil && envelope.Error.Code != "" {
			return newTaskError(envelope.Error.Code, envelope.Error.Message, nil)
		}
		return &TaskError{Code: httpErrorCode(resp.StatusCode), Message: fmt.Sprintf("cloud request failed with status %d", resp.StatusCode)}
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))
	if err != nil {
		return newTaskError("invalid_cloud_response", "cannot read cloud response", err)
	}
	if len(payload) > maxResponseBodyBytes {
		return newTaskError("invalid_cloud_response", "cloud response exceeds the size limit", nil)
	}
	if out == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(out); err != nil {
		return newTaskError("invalid_cloud_response", "cloud returned invalid JSON", nil)
	}
	return nil
}

func httpErrorCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_required"
	case http.StatusForbidden:
		return "resource_access_denied"
	case http.StatusNotFound:
		return "task_not_found"
	case http.StatusConflict:
		return "cloud_conflict"
	case http.StatusUnprocessableEntity:
		return "cloud_rejected"
	default:
		if status >= 500 {
			return "cloud_unavailable"
		}
		return "cloud_request_failed"
	}
}

func retryableReadError(err error) bool {
	var taskErr *TaskError
	if !errors.As(err, &taskErr) {
		return false
	}
	return taskErr.Code == "cloud_unavailable"
}
