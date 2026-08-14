package membertask

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Task struct {
	Key               TaskKey            `json:"task_key"`
	DisplayName       string             `json:"display_name"`
	DisplayNameSource DisplayNameSource  `json:"display_name_source"`
	Remote            *RemoteTask        `json:"remote,omitempty"`
	Local             *TaskRecord        `json:"local,omitempty"`
	Context           *RemoteTaskContext `json:"context,omitempty"`
	Projection        Projection         `json:"projection"`
}

type DisplayNameSource string

const (
	DisplayNameSourceCloud    DisplayNameSource = "cloud"
	DisplayNameSourceIssue    DisplayNameSource = "issue_title"
	DisplayNameSourceNode     DisplayNameSource = "node_name"
	DisplayNameSourceLocal    DisplayNameSource = "local"
	DisplayNameSourceIdentity DisplayNameSource = "task_identity"
)

const maxDisplayNameRunes = 128

type Service struct {
	store     *Store
	cloud     *CloudClient
	now       func() time.Time
	publisher *GitPublisher
	taskLocks sync.Map
}

func NewService(store *Store, cloud *CloudClient) *Service {
	return &Service{store: store, cloud: cloud, now: time.Now, publisher: NewGitPublisher()}
}

func (s *Service) lockTask(key TaskKey) func() {
	value, _ := s.taskLocks.LoadOrStore(key.String(), &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Service) List(ctx context.Context) ([]Task, error) {
	local, err := s.store.List()
	if err != nil {
		return nil, err
	}
	merged := make(map[string]Task, len(local))
	for i := range local {
		record := local[i]
		task := Task{Key: record.Key, Local: &record, Projection: projectRecord(record, "")}
		setTaskDisplay(&task)
		merged[record.Key.String()] = task
	}

	remote, remoteErr := s.cloud.List(ctx)
	if remoteErr != nil {
		if !isCloudUnavailable(remoteErr) {
			return nil, remoteErr
		}
		for raw, task := range merged {
			record, found, err := s.updateTaskForList(task.Key, func(record *TaskRecord) {
				record.Offline = true
				record.CloudStateUnverified = true
			})
			if err != nil {
				return nil, err
			}
			if !found {
				delete(merged, raw)
				continue
			}
			task.Local = &record
			task.Projection = projectRecord(record, "")
			merged[raw] = task
		}
		return sortedTasks(merged), nil
	}

	now := s.now().UTC()
	seen := make(map[string]struct{}, len(remote))
	for i := range remote {
		remoteTask := remote[i]
		key := remoteTask.Key()
		seen[key.String()] = struct{}{}
		task, exists := merged[key.String()]
		if !exists {
			task = Task{Key: key}
		}
		task.Remote = &remoteTask
		if task.Local != nil {
			record, found, err := s.updateTaskForList(key, func(record *TaskRecord) {
				if name, source := displayNameFromRemote(remoteTask); name != "" {
					record.DisplayName, record.DisplayNameSource = name, source
				}
				record.Offline = false
				record.CloudStateUnverified = false
				record.ReadOnly = false
				record.LastVerifiedAt = &now
				record.RemoteVersion = versionString(remoteTask)
				record.Attempt = remoteTask.Attempt
				syncRecordIssue(record, remoteTask)
			})
			if err != nil {
				return nil, err
			}
			if found {
				task.Local = &record
				task.Projection = projectRecord(record, remoteTask.CloudStatus)
			} else {
				task.Local = nil
				task.Projection = ProjectStatus(Facts{Role: key.Role, RemoteState: remoteTask.CloudStatus})
			}
		} else {
			task.Projection = ProjectStatus(Facts{Role: key.Role, RemoteState: remoteTask.CloudStatus})
		}
		setTaskDisplay(&task)
		merged[key.String()] = task
	}
	for raw, task := range merged {
		if _, ok := seen[raw]; ok || task.Local == nil {
			continue
		}
		record, found, err := s.updateTaskForList(task.Key, func(record *TaskRecord) {
			record.Offline = false
			record.CloudStateUnverified = false
			record.ReadOnly = true
			record.LastVerifiedAt = &now
		})
		if err != nil {
			return nil, err
		}
		if !found {
			delete(merged, raw)
			continue
		}
		task.Local = &record
		task.Projection = projectRecord(record, "")
		merged[raw] = task
	}
	for raw, task := range merged {
		setTaskDisplay(&task)
		merged[raw] = task
	}
	return sortedTasks(merged), nil
}

func (s *Service) updateTaskForList(key TaskKey, update func(*TaskRecord)) (TaskRecord, bool, error) {
	unlock := s.lockTask(key)
	defer unlock()
	record, found, err := s.store.LoadTask(key)
	if err != nil || !found {
		return record, found, err
	}
	update(&record)
	if err := s.store.SaveTask(record); err != nil {
		return TaskRecord{}, false, err
	}
	return record, true, nil
}

func (s *Service) Get(ctx context.Context, key TaskKey) (Task, error) {
	unlock := s.lockTask(key)
	defer unlock()
	record, found, err := s.store.LoadTask(key)
	if err != nil {
		return Task{}, err
	}
	remoteContext, err := s.cloud.GetContext(ctx, key)
	if err != nil {
		if !found {
			return Task{}, err
		}
		if !isCloudUnavailable(err) {
			s.handleAuthorityError(record, err)
			return Task{}, err
		}
		if err := refreshTaskManifestState(&record); err != nil {
			return Task{}, err
		}
		record.Offline = true
		record.CloudStateUnverified = true
		if err := s.store.SaveTask(record); err != nil {
			return Task{}, err
		}
		task := Task{Key: key, Local: &record, Projection: projectRecord(record, "")}
		setTaskDisplay(&task)
		return task, nil
	}
	remote := remoteContext.RemoteTask
	task := Task{Key: key, Remote: &remote, Context: &remoteContext}
	if found {
		if err := refreshTaskManifestState(&record); err != nil {
			return Task{}, err
		}
		now := s.now().UTC()
		record.Offline = false
		record.CloudStateUnverified = false
		record.LastVerifiedAt = &now
		record.RemoteVersion = versionString(remote)
		record.Attempt = remote.Attempt
		syncRecordIssue(&record, remote)
		if name, source := displayNameFromRemote(remote); name != "" {
			record.DisplayName, record.DisplayNameSource = name, source
		}
		if err := s.store.SaveTask(record); err != nil {
			return Task{}, err
		}
		task.Local = &record
		task.Projection = projectRecord(record, remote.CloudStatus)
	} else {
		task.Projection = ProjectStatus(Facts{Role: key.Role, RemoteState: remote.CloudStatus})
	}
	setTaskDisplay(&task)
	return task, nil
}

func refreshTaskManifestState(record *TaskRecord) error {
	if !record.Prepared || record.Manifest == nil || record.Directory == "" {
		return nil
	}
	verification, err := VerifyManifest(record.Directory, *record.Manifest)
	if err != nil {
		return err
	}
	record.Dirty = verification.Dirty
	record.DirtyPaths = verification.ChangedPaths
	return nil
}

func displayNameFromRemote(remote RemoteTask) (string, DisplayNameSource) {
	if name := normalizeDisplayName(remote.DisplayName); name != "" {
		return name, DisplayNameSourceCloud
	}
	if name := normalizeDisplayName(remote.Title); name != "" {
		return name, DisplayNameSourceCloud
	}
	if name := normalizeDisplayName(remote.IssueTitle); name != "" {
		return name, DisplayNameSourceIssue
	}
	if name := normalizeDisplayName(remote.NodeName); name != "" {
		return name, DisplayNameSourceNode
	}
	return "", ""
}

func syncRecordIssue(record *TaskRecord, remote RemoteTask) {
	if record == nil {
		return
	}
	record.IssueID = remote.IssueID
	record.IssueNumber = remote.IssueNumber
	record.IssueIdentifier = remote.IssueIdentifier
	record.IssueTitle = remote.IssueTitle
	record.IssueDescription = remote.IssueDescription
	record.WorkspaceSlug = remote.WorkspaceSlug
	record.TaskKind = remote.TaskKind
}

func normalizeDisplayName(value string) string {
	normalized := strings.Join(strings.Fields(value), " ")
	runes := []rune(normalized)
	if len(runes) > maxDisplayNameRunes {
		normalized = string(runes[:maxDisplayNameRunes])
	}
	return normalized
}

func setTaskDisplay(task *Task) {
	if task == nil {
		return
	}
	if task.Remote != nil {
		if name, source := displayNameFromRemote(*task.Remote); name != "" {
			task.DisplayName, task.DisplayNameSource = name, source
			return
		}
	}
	if task.Local != nil && strings.TrimSpace(task.Local.DisplayName) != "" {
		task.DisplayName, task.DisplayNameSource = task.Local.DisplayName, DisplayNameSourceLocal
		return
	}
	task.DisplayName = strings.Join([]string{task.Key.WorkspaceID, task.Key.NodeRunID, string(task.Key.Role)}, "/")
	task.DisplayNameSource = DisplayNameSourceIdentity
}

func isCloudUnavailable(err error) bool {
	var taskErr *TaskError
	return errors.As(err, &taskErr) && taskErr.Code == "cloud_unavailable"
}

func (s *Service) Refresh(ctx context.Context, key TaskKey) (TaskRecord, error) {
	unlock := s.lockTask(key)
	defer unlock()
	_ = ctx
	record, found, err := s.store.LoadTask(key)
	if err != nil {
		return TaskRecord{}, err
	}
	if !found {
		return TaskRecord{}, newTaskError("task_not_found", "local task is not prepared", nil)
	}
	if record.Manifest == nil || record.Directory == "" {
		return TaskRecord{}, newTaskError("local_task_store_corrupt", "prepared task has no manifest or directory", nil)
	}
	verification, err := VerifyManifest(record.Directory, *record.Manifest)
	if err != nil {
		return TaskRecord{}, err
	}
	record.Dirty = verification.Dirty
	record.DirtyPaths = verification.ChangedPaths
	if err := s.store.SaveTask(record); err != nil {
		return TaskRecord{}, err
	}
	return record, nil
}

func (s *Service) Reconcile(ctx context.Context, key TaskKey) error {
	if err := s.RecoverPrepareJournals(ctx); err != nil {
		return err
	}
	if err := s.RecoverDeleteJournals(ctx); err != nil {
		return err
	}
	record, found, err := s.store.LoadTask(key)
	if err != nil || !found || record.Operation == nil || record.Operation.ID == "" {
		return err
	}
	_, err = s.RecoverOperation(ctx, key)
	return err
}

func (s *Service) ReconcileAll(ctx context.Context) error {
	if err := s.RecoverPrepareJournals(ctx); err != nil {
		return err
	}
	if err := s.RecoverDeleteJournals(ctx); err != nil {
		return err
	}
	records, err := s.store.List()
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Operation == nil || record.Operation.ID == "" {
			continue
		}
		if _, err := s.RecoverOperation(ctx, record.Key); err != nil {
			return err
		}
	}
	return nil
}

func projectRecord(record TaskRecord, remoteState string) Projection {
	return ProjectStatus(Facts{
		Ended: record.Ended, ReadOnly: record.ReadOnly,
		ReprepareRequired: record.ReprepareRequired, ReconfirmationRequired: record.ReconfirmationRequired,
		SyncPending: record.SyncPending, Prepared: record.Prepared, Role: record.Key.Role,
		RemoteState: remoteState, WriteAuthorityLost: record.WriteAuthorityLost,
		Flags: Flags{Offline: record.Offline, Dirty: record.Dirty, CloudStateUnverified: record.CloudStateUnverified,
			WonByOtherOperation: record.WonByOtherOperation, CleanupPending: record.CleanupPending},
	})
}

func ProjectTaskRecord(record TaskRecord) Projection {
	return projectRecord(record, "")
}

func sortedTasks(tasks map[string]Task) []Task {
	out := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.String() < out[j].Key.String() })
	return out
}

func versionString(task RemoteTask) string {
	return fmt.Sprintf("%d:%d:%d", task.Attempt, task.TaskVersion, task.ContextVersion)
}
