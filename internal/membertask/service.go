package membertask

import (
	"context"
	"fmt"
	"sort"
	"time"
)

type Task struct {
	Key        TaskKey            `json:"task_key"`
	Remote     *RemoteTask        `json:"remote,omitempty"`
	Local      *TaskRecord        `json:"local,omitempty"`
	Context    *RemoteTaskContext `json:"context,omitempty"`
	Projection Projection         `json:"projection"`
}

type Service struct {
	store     *Store
	cloud     *CloudClient
	now       func() time.Time
	publisher *GitPublisher
}

func NewService(store *Store, cloud *CloudClient) *Service {
	return &Service{store: store, cloud: cloud, now: time.Now, publisher: NewGitPublisher()}
}

func (s *Service) List(ctx context.Context) ([]Task, error) {
	local, err := s.store.List()
	if err != nil {
		return nil, err
	}
	merged := make(map[string]Task, len(local))
	for i := range local {
		record := local[i]
		merged[record.Key.String()] = Task{Key: record.Key, Local: &record, Projection: projectRecord(record, "")}
	}

	remote, remoteErr := s.cloud.List(ctx)
	if remoteErr != nil {
		for raw, task := range merged {
			record := *task.Local
			record.Offline = true
			record.CloudStateUnverified = true
			task.Local = &record
			task.Projection = projectRecord(record, "")
			merged[raw] = task
		}
		return sortedTasks(merged), nil
	}

	now := s.now().UTC()
	for i := range remote {
		remoteTask := remote[i]
		key := remoteTask.Key()
		task, exists := merged[key.String()]
		if !exists {
			task = Task{Key: key}
		}
		task.Remote = &remoteTask
		if task.Local != nil {
			record := *task.Local
			record.Offline = false
			record.CloudStateUnverified = false
			record.LastVerifiedAt = &now
			record.RemoteVersion = versionString(remoteTask)
			record.Attempt = remoteTask.Attempt
			task.Local = &record
			task.Projection = projectRecord(record, remoteTask.CloudStatus)
		} else {
			task.Projection = ProjectStatus(Facts{Role: key.Role, RemoteState: remoteTask.CloudStatus})
		}
		merged[key.String()] = task
	}
	return sortedTasks(merged), nil
}

func (s *Service) Get(ctx context.Context, key TaskKey) (Task, error) {
	record, found, err := s.store.LoadTask(key)
	if err != nil {
		return Task{}, err
	}
	remoteContext, err := s.cloud.GetContext(ctx, key)
	if err != nil {
		if !found {
			return Task{}, err
		}
		record.Offline = true
		record.CloudStateUnverified = true
		return Task{Key: key, Local: &record, Projection: projectRecord(record, "")}, nil
	}
	remote := remoteContext.RemoteTask
	task := Task{Key: key, Remote: &remote, Context: &remoteContext}
	if found {
		now := s.now().UTC()
		record.Offline = false
		record.CloudStateUnverified = false
		record.LastVerifiedAt = &now
		record.RemoteVersion = versionString(remote)
		record.Attempt = remote.Attempt
		task.Local = &record
		task.Projection = projectRecord(record, remote.CloudStatus)
	} else {
		task.Projection = ProjectStatus(Facts{Role: key.Role, RemoteState: remote.CloudStatus})
	}
	return task, nil
}

func (s *Service) Start(ctx context.Context, key TaskKey) (TaskRecord, error) {
	_ = ctx
	record, found, err := s.store.LoadTask(key)
	if err != nil {
		return TaskRecord{}, err
	}
	if !found {
		return TaskRecord{}, newTaskError("task_not_found", "local task is not prepared", nil)
	}
	if !record.Prepared || (record.Activity != ActivityPrepared && record.Activity != ActivityPaused && record.Activity != ActivityActive) {
		return TaskRecord{}, newTaskError("invalid_task_state", "task cannot be started from its current state", nil)
	}
	if record.Activity == ActivityActive {
		return record, nil
	}
	record.Activity = ActivityActive
	if err := s.store.SaveTask(record); err != nil {
		return TaskRecord{}, err
	}
	return record, nil
}

func (s *Service) Pause(ctx context.Context, key TaskKey) (TaskRecord, error) {
	_ = ctx
	record, found, err := s.store.LoadTask(key)
	if err != nil {
		return TaskRecord{}, err
	}
	if !found {
		return TaskRecord{}, newTaskError("task_not_found", "local task is not prepared", nil)
	}
	if record.Activity == ActivityPaused {
		return record, nil
	}
	if record.Activity != ActivityActive {
		return TaskRecord{}, newTaskError("invalid_task_state", "only an active task can be paused", nil)
	}
	record.Activity = ActivityPaused
	if err := s.store.SaveTask(record); err != nil {
		return TaskRecord{}, err
	}
	return record, nil
}

func (s *Service) Refresh(ctx context.Context, key TaskKey) (TaskRecord, error) {
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
		SyncPending: record.SyncPending, Prepared: record.Prepared, Activity: record.Activity, Role: record.Key.Role,
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
