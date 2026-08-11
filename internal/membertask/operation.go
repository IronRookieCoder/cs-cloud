package membertask

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type RepoPublishPlan struct {
	RepositoryIdentity string         `json:"repository_identity"`
	ExpectedRef        string         `json:"expected_ref"`
	BeforeSHA          string         `json:"before_sha"`
	HeadSHA            string         `json:"head_sha"`
	RemoteName         string         `json:"remote_name,omitempty"`
	OperationMarker    string         `json:"operation_marker,omitempty"`
	Status             RepoStepStatus `json:"status,omitempty"`
}

type Preview struct {
	ID               string          `json:"id"`
	Kind             string          `json:"kind"`
	Status           string          `json:"status"`
	TaskVersion      int64           `json:"task_version"`
	ContextVersion   int64           `json:"context_version"`
	Attempt          int             `json:"attempt"`
	MaterialDigest   string          `json:"material_digest"`
	ContentDigest    string          `json:"content_digest"`
	RiskDigest       string          `json:"risk_digest,omitempty"`
	PreviewDigest    string          `json:"preview_digest,omitempty"`
	ReviewSnapshotID string          `json:"review_snapshot_id,omitempty"`
	ExpiresAt        time.Time       `json:"expires_at"`
	PublishPlan      json.RawMessage `json:"publish_plan,omitempty"`
}

type OperationStatus string

const (
	OperationPreviewed         OperationStatus = "previewed"
	OperationAccepted          OperationStatus = "accepted"
	OperationRunning           OperationStatus = "running"
	OperationCompleted         OperationStatus = "completed"
	OperationRepreviewRequired OperationStatus = "repreview_required"
	OperationConflict          OperationStatus = "conflict"
	OperationUnknown           OperationStatus = "unknown"
	OperationFailed            OperationStatus = "failed"
)

type Operation struct {
	ID           string                    `json:"id"`
	PreviewID    string                    `json:"preview_id"`
	Kind         string                    `json:"kind"`
	Status       OperationStatus           `json:"status"`
	PublishPlan  json.RawMessage           `json:"publish_plan,omitempty"`
	PublishSteps []RepoPublishPlan         `json:"steps,omitempty"`
	LocalSteps   map[string]RepoStepResult `json:"local_steps,omitempty"`
	Outcome      Outcome                   `json:"outcome,omitempty"`
}

type SubmitRepository struct {
	RepositoryIdentity string   `json:"repository_identity"`
	ExpectedRef        string   `json:"ref"`
	BaseRef            string   `json:"base_ref"`
	BaseSHA            string   `json:"base_sha"`
	BeforeSHA          string   `json:"before_sha"`
	HeadSHA            string   `json:"head_sha"`
	ChangedPaths       []string `json:"changed_paths"`
	OperationMarker    string   `json:"operation_marker"`
}

type SubmitFile struct {
	Identity      string `json:"deliverable_id"`
	SourceVersion string `json:"version"`
	RelativePath  string `json:"relative_path"`
	SHA256        string `json:"sha256"`
	Content       string `json:"content"`
}

type SubmitPreviewRequest struct {
	Attempt        int                `json:"attempt"`
	TaskVersion    int64              `json:"task_version"`
	ContextVersion int64              `json:"context_version"`
	MaterialDigest string             `json:"material_digest"`
	ContentDigest  string             `json:"content_digest"`
	Repositories   []SubmitRepository `json:"repositories"`
	Files          []SubmitFile       `json:"files"`
}

type ReviewPreviewRequest struct {
	Attempt        int    `json:"attempt"`
	TaskVersion    int64  `json:"task_version"`
	ContextVersion int64  `json:"context_version"`
	MaterialDigest string `json:"material_digest"`
	ContentDigest  string `json:"content_digest"`
	Decision       string `json:"decision"`
	Reason         string `json:"reason,omitempty"`
}

type StepReport struct {
	RepositoryIdentity string         `json:"repository_identity"`
	Status             RepoStepStatus `json:"status"`
	ObservedSHA        string         `json:"observed_sha,omitempty"`
	ErrorCode          string         `json:"error_code,omitempty"`
}

func (s *Service) PreviewSubmit(ctx context.Context, key TaskKey) (Preview, error) {
	if key.Role != RoleWorker {
		return Preview{}, newTaskError("invalid_task_role", "only workers can preview a submission", nil)
	}
	record, verification, err := s.loadVerifiedTask(key)
	if err != nil {
		return Preview{}, err
	}
	attempt, taskVersion, contextVersion, err := parseRemoteVersion(record.RemoteVersion)
	if err != nil {
		return Preview{}, err
	}
	repositories, files, err := buildSubmitManifest(record, verification)
	if err != nil {
		return Preview{}, err
	}
	request := SubmitPreviewRequest{Attempt: attempt, TaskVersion: taskVersion, ContextVersion: contextVersion, MaterialDigest: effectiveMaterialDigest(record), ContentDigest: verification.ContentDigest, Repositories: repositories, Files: files}
	preview, err := s.cloud.PreviewSubmit(ctx, key, request)
	if err != nil {
		s.handleAuthorityError(record, err)
		return Preview{}, err
	}
	if preview.ContentDigest == "" {
		preview.ContentDigest = request.ContentDigest
	}
	if preview.RiskDigest == "" {
		preview.RiskDigest = preview.PreviewDigest
	}
	if err := validatePreview(preview, request.Attempt, request.TaskVersion, request.ContextVersion, request.MaterialDigest, request.ContentDigest); err != nil {
		return Preview{}, err
	}
	record.Preview = &preview
	if err := s.store.SaveTask(record); err != nil {
		return Preview{}, err
	}
	return preview, nil
}

func (s *Service) PreviewReview(ctx context.Context, key TaskKey, decision, reason string) (Preview, error) {
	if key.Role != RoleCritic {
		return Preview{}, newTaskError("invalid_task_role", "only critics can preview a review", nil)
	}
	if decision != "approve" && decision != "reject" {
		return Preview{}, newTaskError("invalid_arguments", "review decision must be approve or reject", nil)
	}
	if decision == "reject" && strings.TrimSpace(reason) == "" {
		return Preview{}, newTaskError("invalid_arguments", "reject requires a reason", nil)
	}
	record, verification, err := s.loadVerifiedTask(key)
	if err != nil {
		return Preview{}, err
	}
	attempt, taskVersion, contextVersion, err := parseRemoteVersion(record.RemoteVersion)
	if err != nil {
		return Preview{}, err
	}
	request := ReviewPreviewRequest{Attempt: attempt, TaskVersion: taskVersion, ContextVersion: contextVersion, MaterialDigest: effectiveMaterialDigest(record), ContentDigest: verification.ContentDigest, Decision: decision, Reason: reason}
	preview, err := s.cloud.PreviewReview(ctx, key, request)
	if err != nil {
		s.handleAuthorityError(record, err)
		return Preview{}, err
	}
	if preview.ContentDigest == "" {
		preview.ContentDigest = request.ContentDigest
	}
	if preview.RiskDigest == "" {
		preview.RiskDigest = preview.PreviewDigest
	}
	if err := validatePreview(preview, attempt, taskVersion, contextVersion, request.MaterialDigest, request.ContentDigest); err != nil {
		return Preview{}, err
	}
	record.Preview = &preview
	if err := s.store.SaveTask(record); err != nil {
		return Preview{}, err
	}
	return preview, nil
}

func (s *Service) ConfirmOperation(ctx context.Context, key TaskKey, previewID string) (Operation, error) {
	unlock := s.lockTask(key)
	defer unlock()

	record, verification, err := s.loadVerifiedTask(key)
	if err != nil {
		return Operation{}, err
	}
	if record.Preview == nil || record.Preview.ID != previewID {
		return Operation{}, newTaskError("preview_stale", "preview does not match the local task", nil)
	}
	if !record.Preview.ExpiresAt.IsZero() && !s.now().Before(record.Preview.ExpiresAt) {
		return Operation{}, newTaskError("preview_stale", "preview has expired", nil)
	}
	if record.Preview.MaterialDigest != effectiveMaterialDigest(record) || record.Preview.ContentDigest != verification.ContentDigest {
		return Operation{}, newTaskError("preview_stale", "task content changed after preview", nil)
	}
	operation, err := s.cloud.ConfirmOperation(ctx, key, previewID)
	if err != nil {
		s.handleAuthorityError(record, err)
		return Operation{}, err
	}
	if operation.PreviewID == "" {
		operation.PreviewID = previewID
	}
	if operation.PreviewID != previewID || operation.ID == "" {
		return Operation{}, newTaskError("invalid_cloud_response", "confirmed operation identity is invalid", nil)
	}
	if operation.LocalSteps == nil {
		operation.LocalSteps = map[string]RepoStepResult{}
	}
	record.Operation = &operation
	if err := s.store.SaveTask(record); err != nil {
		return Operation{}, err
	}
	return s.executeOperation(ctx, record)
}

func effectiveMaterialDigest(record TaskRecord) string {
	if record.CloudMaterialDigest != "" {
		return record.CloudMaterialDigest
	}
	if record.Manifest != nil {
		return record.Manifest.MaterialDigest
	}
	return ""
}

func (s *Service) RecoverOperation(ctx context.Context, key TaskKey) (Operation, error) {
	record, found, err := s.store.LoadTask(key)
	if err != nil {
		return Operation{}, err
	}
	if !found || record.Operation == nil || record.Operation.ID == "" {
		return Operation{}, newTaskError("operation_not_found", "task has no accepted operation", nil)
	}
	operation, err := s.cloud.GetOperation(ctx, key, record.Operation.ID)
	if err != nil {
		return Operation{}, err
	}
	if operation.ID != record.Operation.ID {
		return Operation{}, newTaskError("invalid_cloud_response", "operation identity changed during recovery", nil)
	}
	record.Operation = &operation
	if operation.Status == OperationCompleted {
		record.Ended = true
		record.SyncPending = false
		if err := s.store.SaveTask(record); err != nil {
			return Operation{}, err
		}
		return operation, nil
	}
	if operation.Status != OperationAccepted && operation.Status != OperationRunning {
		if err := s.store.SaveTask(record); err != nil {
			return Operation{}, err
		}
		return operation, nil
	}
	return s.executeOperation(ctx, record)
}

func (s *Service) executeOperation(ctx context.Context, record TaskRecord) (Operation, error) {
	operation := record.Operation
	if operation == nil {
		return Operation{}, newTaskError("operation_not_found", "accepted operation is missing", nil)
	}
	if operation.LocalSteps == nil {
		operation.LocalSteps = map[string]RepoStepResult{}
	}
	if len(operation.PublishSteps) == 0 {
		return *operation, nil
	}
	publisher := s.publisher
	if publisher == nil {
		publisher = NewGitPublisher()
	}
	written := false
	for _, plan := range operation.PublishSteps {
		if step, ok := operation.LocalSteps[plan.RepositoryIdentity]; ok && step.Status == RepoStepVerified {
			written = true
			continue
		}
		repo, ok := repositoryByIdentity(record.Manifest, plan.RepositoryIdentity)
		if !ok || repo.TargetRef != plan.ExpectedRef {
			return *operation, newTaskError("invalid_publish_plan", "cloud publish plan does not match prepared repository", nil)
		}
		worktree := filepath.Join(record.Directory, filepath.FromSlash(repo.RelativePath))
		operation.Status = OperationRunning
		operation.LocalSteps[plan.RepositoryIdentity] = RepoStepResult{RepositoryIdentity: plan.RepositoryIdentity, Status: RepoStepRunning}
		record.Operation = operation
		if err := s.store.SaveTask(record); err != nil {
			return *operation, err
		}
		result, err := publisher.PublishStep(ctx, worktree, plan)
		operation.LocalSteps[plan.RepositoryIdentity] = result
		record.Operation = operation
		_ = s.store.SaveTask(record)
		if err != nil {
			var taskErr *TaskError
			errorCode := "operation_result_unknown"
			if errors.As(err, &taskErr) {
				errorCode = taskErr.Code
			}
			updated, reportErr := s.cloud.ReportOperation(ctx, record.Key, operation.ID, StepReport{RepositoryIdentity: result.RepositoryIdentity, Status: result.Status, ObservedSHA: result.ObservedSHA, ErrorCode: errorCode})
			if reportErr != nil {
				return *operation, newTaskError("operation_result_unknown", "operation failure report result is unknown", reportErr)
			}
			if updated.ID != "" {
				updated.LocalSteps = operation.LocalSteps
				operation = &updated
				record.Operation = operation
				_ = s.store.SaveTask(record)
			}
			if errorCode == "remote_ref_changed" && written {
				return *operation, newTaskError("operation_ref_conflict", "remote ref changed after another repository was published", nil)
			}
			return *operation, err
		}
		written = true
		updated, err := s.cloud.ReportOperation(ctx, record.Key, operation.ID, StepReport{RepositoryIdentity: result.RepositoryIdentity, Status: result.Status, ObservedSHA: result.ObservedSHA})
		if err != nil {
			return *operation, newTaskError("operation_result_unknown", "operation report result is unknown", err)
		}
		if updated.ID != "" {
			operation = &updated
			if operation.LocalSteps == nil {
				operation.LocalSteps = record.Operation.LocalSteps
			}
		}
	}
	record.Operation = operation
	if operation.Status == OperationCompleted {
		record.Ended = true
	}
	if err := s.store.SaveTask(record); err != nil {
		return *operation, err
	}
	return *operation, nil
}

func (s *Service) loadVerifiedTask(key TaskKey) (TaskRecord, Verification, error) {
	record, found, err := s.store.LoadTask(key)
	if err != nil {
		return TaskRecord{}, Verification{}, err
	}
	if !found || !record.Prepared || record.Manifest == nil {
		return TaskRecord{}, Verification{}, newTaskError("task_not_prepared", "task must be prepared first", nil)
	}
	verification, err := VerifyManifest(record.Directory, *record.Manifest)
	if err != nil {
		return TaskRecord{}, Verification{}, err
	}
	record.Dirty = verification.Dirty
	record.DirtyPaths = verification.ChangedPaths
	return record, verification, nil
}

func buildSubmitManifest(record TaskRecord, verification Verification) ([]SubmitRepository, []SubmitFile, error) {
	var repositories []SubmitRepository
	for _, repo := range record.Manifest.Repositories {
		repoDir := filepath.Join(record.Directory, filepath.FromSlash(repo.RelativePath))
		status, err := gitOutputAllowEmpty(repoDir, "status", "--porcelain")
		if err != nil {
			return nil, nil, newTaskError("git_material_invalid", "cannot inspect repository worktree", err)
		}
		if strings.TrimSpace(status) != "" {
			return nil, nil, newTaskError("uncommitted_changes_present", "repository index and worktree must be clean before preview", nil)
		}
		changed, head, err := verifyRepository(repoDir, repo)
		if err != nil {
			return nil, nil, err
		}
		repositories = append(repositories, SubmitRepository{RepositoryIdentity: repo.Identity, ExpectedRef: repo.TargetRef, BaseRef: repo.BaseRef, BaseSHA: repo.BaseSHA, BeforeSHA: repo.BeforeSHA, HeadSHA: head, ChangedPaths: changed, OperationMarker: digestJSON(struct{ Key, Repo, Head string }{record.Key.String(), repo.Identity, head})})
	}
	var files []SubmitFile
	for _, file := range record.Manifest.Files {
		if file.Role != MaterialOutputWritable {
			continue
		}
		path := filepath.Join(record.Directory, filepath.FromSlash(file.RelativePath))
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		digest := sha256.Sum256(content)
		files = append(files, SubmitFile{Identity: file.Identity, SourceVersion: file.SourceVersion, RelativePath: file.RelativePath, SHA256: hex.EncodeToString(digest[:]), Content: string(content)})
	}
	sort.Slice(repositories, func(i, j int) bool { return repositories[i].RepositoryIdentity < repositories[j].RepositoryIdentity })
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	_ = verification
	return repositories, files, nil
}

func validatePreview(preview Preview, attempt int, taskVersion, contextVersion int64, materialDigest, contentDigest string) error {
	if preview.ID == "" || preview.Attempt != attempt || preview.TaskVersion != taskVersion || preview.ContextVersion != contextVersion || preview.MaterialDigest != materialDigest || preview.ContentDigest != contentDigest {
		return newTaskError("invalid_cloud_response", "cloud preview does not match the current task", nil)
	}
	return nil
}

func parseRemoteVersion(raw string) (int, int64, int64, error) {
	var attempt int
	var taskVersion, contextVersion int64
	if _, err := fmt.Sscanf(raw, "%d:%d:%d", &attempt, &taskVersion, &contextVersion); err != nil || attempt < 1 || taskVersion < 1 || contextVersion < 1 {
		return 0, 0, 0, newTaskError("local_task_store_corrupt", "task version is invalid", err)
	}
	return attempt, taskVersion, contextVersion, nil
}

func repositoryByIdentity(manifest *Manifest, identity string) (RepositoryManifest, bool) {
	if manifest == nil {
		return RepositoryManifest{}, false
	}
	for _, repo := range manifest.Repositories {
		if repo.Identity == identity {
			return repo, true
		}
	}
	return RepositoryManifest{}, false
}

func (s *Service) handleAuthorityError(record TaskRecord, err error) {
	te, ok := err.(*TaskError)
	if !ok {
		return
	}
	switch te.Code {
	case "attempt_won_by_other_operation":
		record.WonByOtherOperation = true
		record.ReadOnly = true
	case "resource_access_denied", "task_not_assigned":
		if record.Operation != nil {
			record.WriteAuthorityLost = true
		} else {
			record.ReadOnly = true
		}
	default:
		return
	}
	_ = s.store.SaveTask(record)
}
