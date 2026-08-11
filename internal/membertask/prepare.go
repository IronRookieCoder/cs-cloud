package membertask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type preparedMetadata struct {
	SchemaVersion       string            `json:"schema_version"`
	Key                 TaskKey           `json:"key"`
	RemoteVersion       string            `json:"remote_version"`
	CloudMaterialDigest string            `json:"cloud_material_digest,omitempty"`
	Attempt             int               `json:"attempt"`
	Goal                string            `json:"goal,omitempty"`
	Acceptance          []string          `json:"acceptance_criteria,omitempty"`
	ReworkReason        string            `json:"rework_reason,omitempty"`
	DisplayName         string            `json:"display_name,omitempty"`
	DisplayNameSource   DisplayNameSource `json:"display_name_source,omitempty"`
	PreparationRoot     string            `json:"preparation_root,omitempty"`
}

type PrepareOptions struct {
	WorkDir string `json:"workdir,omitempty"`
}

var gitCommandContext = exec.CommandContext

var (
	gitObjectIDPattern = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
	gitANSISequence    = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	gitURLUserInfo     = regexp.MustCompile(`(https://)[^/@\s]+@`)
)

const maxGitDiagnosticBytes = 4 << 10

func (s *Service) PrepareWithFacts(ctx context.Context, key TaskKey, option PrepareOptions) (LocalTransition, error) {
	unlock := s.lockTask(key)
	defer unlock()

	existing, found, err := s.store.LoadTask(key)
	if err != nil {
		return LocalTransition{}, err
	}
	record, err := s.prepare(ctx, key, option)
	if err != nil {
		return LocalTransition{}, err
	}
	if found && existing.Prepared && existing.RemoteVersion == record.RemoteVersion && existing.Directory == record.Directory {
		return LocalTransition{TaskRecord: record, Outcome: OutcomeAlreadyCompleted}, nil
	}
	return LocalTransition{TaskRecord: record, Outcome: OutcomeCompleted, Performed: true}, nil
}

func (s *Service) Prepare(ctx context.Context, key TaskKey) (TaskRecord, error) {
	unlock := s.lockTask(key)
	defer unlock()
	return s.prepare(ctx, key, PrepareOptions{})
}

func (s *Service) prepare(ctx context.Context, key TaskKey, options PrepareOptions) (TaskRecord, error) {
	remote, err := s.cloud.GetContext(ctx, key)
	if err != nil {
		return TaskRecord{}, err
	}
	if !remote.PrepareAllowed || !providersSupported(remote.Providers) {
		return TaskRecord{}, newTaskError("provider_not_supported", "only Gitea task materials can be prepared", nil)
	}
	sources, err := prepareSources(remote, key)
	if err != nil {
		return TaskRecord{}, err
	}
	preparationRoot, final, err := s.prepareTarget(key, options.WorkDir)
	if err != nil {
		return TaskRecord{}, err
	}
	displayName, displaySource := displayNameFromRemote(remote.RemoteTask)
	var archive string
	var archiveRoot string
	if existing, found, err := s.store.LoadTask(key); err != nil {
		return TaskRecord{}, err
	} else if found && existing.Directory != "" {
		if displayName == "" && existing.DisplayName != "" {
			displayName, displaySource = existing.DisplayName, existing.DisplayNameSource
		}
		if options.WorkDir == "" && existing.Directory != "" {
			preparationRoot = existing.PreparationRoot
			if preparationRoot == "" {
				preparationRoot = s.store.Layout().TasksRoot()
			}
			final = existing.Directory
		}
		if err := ValidateContainedPath(preparationRoot, final); err != nil {
			return TaskRecord{}, newTaskError("local_task_store_corrupt", "prepared task directory is outside its managed root", err)
		}
		if options.WorkDir != "" && !samePath(existing.Directory, final) {
			return TaskRecord{}, newTaskError("prepare_location_conflict", fmt.Sprintf("task is already prepared at %s", existing.Directory), nil)
		}
		displayChanged := syncRecordDisplay(&existing, remote.RemoteTask)
		if existing.RemoteVersion == versionString(remote.RemoteTask) && existing.Prepared {
			if displayChanged {
				if err := updatePreparedMetadata(s.store, existing, remote); err != nil {
					return TaskRecord{}, err
				}
				if err := s.store.SaveTask(existing); err != nil {
					return TaskRecord{}, err
				}
			}
			return existing, nil
		}
		oldAttempt, _, oldContextVersion, versionErr := parseRemoteVersion(existing.RemoteVersion)
		if versionErr != nil {
			return TaskRecord{}, versionErr
		}
		if remote.Attempt < oldAttempt {
			return TaskRecord{}, newTaskError("invalid_cloud_response", "cloud task attempt moved backwards", nil)
		}
		if remote.Attempt > oldAttempt && existing.Directory != "" {
			if existing.Operation != nil && existing.Operation.Status != OperationCompleted {
				return TaskRecord{}, newTaskError("operation_recovery_required", "accepted operation must be recovered before rework", nil)
			}
			existing.Attempt = remote.Attempt
			existing.RemoteVersion = versionString(remote.RemoteTask)
			existing.CloudMaterialDigest = remote.MaterialDigest
			existing.ReconfirmationRequired = false
			existing.Preview = nil
			existing.Operation = nil
			existing.Ended = false
			if err := updatePreparedMetadata(s.store, existing, remote); err != nil {
				return TaskRecord{}, err
			}
			if err := s.store.SaveTask(existing); err != nil {
				return TaskRecord{}, err
			}
			return existing, nil
		}
		if int64(remote.ContextVersion) == oldContextVersion {
			existing.RemoteVersion = versionString(remote.RemoteTask)
			existing.CloudMaterialDigest = remote.MaterialDigest
			if err := updatePreparedMetadata(s.store, existing, remote); err != nil {
				return TaskRecord{}, err
			}
			if err := s.store.SaveTask(existing); err != nil {
				return TaskRecord{}, err
			}
			return existing, nil
		}
		if existing.Operation != nil && existing.Operation.Status != OperationCompleted {
			return TaskRecord{}, newTaskError("operation_recovery_required", "accepted operation must be recovered before reprepare", nil)
		}
		if existing.Manifest == nil {
			return TaskRecord{}, newTaskError("local_task_store_corrupt", "prepared task has no manifest", nil)
		}
		verification, err := VerifyManifest(existing.Directory, *existing.Manifest)
		if err != nil {
			return TaskRecord{}, err
		}
		if verification.Dirty || existing.Dirty {
			return TaskRecord{}, newTaskError("local_changes_present", "local task has changes that must be preserved manually", nil)
		}
		archiveRoot = s.store.Layout().HistoryRoot()
		if !samePath(preparationRoot, s.store.Layout().TasksRoot()) {
			archiveRoot = filepath.Join(preparationRoot, ".history")
		}
		archive = filepath.Join(archiveRoot, key.CloudInstanceID, key.WorkspaceID, key.NodeRunID+"-"+string(key.Role), fmt.Sprintf("attempt-%d-context-%d", oldAttempt, oldContextVersion))
	}

	if _, err := os.Lstat(final); err == nil {
		if archive == "" {
			return TaskRecord{}, newTaskError("local_task_store_corrupt", "task directory exists without an index record", nil)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return TaskRecord{}, newTaskError("local_task_store_unavailable", "cannot inspect task directory", err)
	}
	parent := filepath.Dir(final)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return TaskRecord{}, newTaskError("local_task_store_unavailable", "cannot create task parent directory", err)
	}
	if !samePath(preparationRoot, s.store.Layout().TasksRoot()) {
		if err := ensureManagedTaskRoot(preparationRoot); err != nil {
			return TaskRecord{}, err
		}
	}
	prepareID := digestJSON(struct {
		Key     string
		Version string
	}{key.String(), versionString(remote.RemoteTask)})[:24]
	staging := filepath.Join(parent, "."+filepath.Base(final)+".staging-"+prepareID)
	journal := PrepareJournal{ID: prepareID, Key: key, Phase: PreparePhasePreparing, Staging: staging, Final: final, Root: preparationRoot, Archive: archive, ArchiveRoot: archiveRoot, CreatedAt: s.now().UTC()}
	if err := s.savePrepareJournal(journal); err != nil {
		return TaskRecord{}, err
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return TaskRecord{}, newTaskError("prepare_failed", "cannot create task staging directory", err)
	}
	if err := materializeSources(ctx, staging, sources); err != nil {
		_ = os.RemoveAll(staging)
		_ = s.removePrepareJournal(journal.ID)
		return TaskRecord{}, err
	}
	manifest, err := BuildManifest(staging, sources)
	if err != nil {
		_ = os.RemoveAll(staging)
		_ = s.removePrepareJournal(journal.ID)
		return TaskRecord{}, err
	}
	metadata := preparedMetadata{SchemaVersion: SchemaVersion, Key: key, RemoteVersion: versionString(remote.RemoteTask), CloudMaterialDigest: remote.MaterialDigest, Attempt: remote.Attempt, Goal: remote.Goal, Acceptance: remote.AcceptanceCriteria, ReworkReason: remote.ReworkReason, DisplayName: displayName, DisplayNameSource: displaySource, PreparationRoot: preparationRoot}
	if err := writePreparedFiles(s.store, staging, metadata, manifest); err != nil {
		_ = os.RemoveAll(staging)
		_ = s.removePrepareJournal(journal.ID)
		return TaskRecord{}, err
	}
	journal.Phase = PreparePhasePublishReady
	if err := s.savePrepareJournal(journal); err != nil {
		return TaskRecord{}, err
	}
	if archive != "" {
		if pathExists(archive) {
			return TaskRecord{}, newTaskError("local_task_store_corrupt", "reprepare archive already exists", nil)
		}
		if err := os.MkdirAll(filepath.Dir(archive), 0o700); err != nil {
			return TaskRecord{}, err
		}
		if err := os.Rename(final, archive); err != nil {
			return TaskRecord{}, newTaskError("prepare_failed", "cannot archive previous task directory", err)
		}
	}
	if err := os.Rename(staging, final); err != nil {
		return TaskRecord{}, newTaskError("prepare_failed", "cannot publish prepared task directory", err)
	}
	record := TaskRecord{Key: key, DisplayName: metadata.DisplayName, DisplayNameSource: metadata.DisplayNameSource, RemoteVersion: metadata.RemoteVersion, CloudMaterialDigest: metadata.CloudMaterialDigest, Attempt: remote.Attempt, Directory: final, PreparationRoot: preparationRoot, Prepared: true, Activity: ActivityPrepared, Manifest: &manifest}
	if err := s.store.SaveTask(record); err != nil {
		return TaskRecord{}, err
	}
	journal.Phase = PreparePhaseCompleted
	if err := s.savePrepareJournal(journal); err != nil {
		return TaskRecord{}, err
	}
	_ = s.removePrepareJournal(journal.ID)
	return record, nil
}

func (s *Service) RecoverPrepareJournals(ctx context.Context) error {
	root := s.store.Layout().PrepareJournalsRoot()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return newTaskError("local_task_store_unavailable", "cannot list prepare journals", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var recoveryErr error
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return appendRecoveryError(recoveryErr, err)
		}
		var journal PrepareJournal
		path := filepath.Join(root, entry.Name())
		if err := s.store.readJSON(path, &journal); err != nil {
			recoveryErr = appendRecoveryError(recoveryErr, newTaskError("local_task_store_corrupt", "prepare journal is invalid", err))
			recoveryErr = appendRecoveryError(recoveryErr, quarantineCorruptJournal(path))
			continue
		}
		unlock := s.lockTask(journal.Key)
		err := s.recoverPrepareJournal(journal)
		unlock()
		if err != nil {
			recoveryErr = appendRecoveryError(recoveryErr, err)
		}
	}
	return recoveryErr
}

func (s *Service) recoverPrepareJournal(journal PrepareJournal) error {
	if err := validateJournalID(journal.ID); err != nil {
		return err
	}
	tasksRoot := journal.Root
	if tasksRoot == "" {
		tasksRoot = s.store.Layout().TasksRoot()
	}
	if err := ValidateContainedPath(tasksRoot, journal.Staging); err != nil {
		return err
	}
	if err := ValidateContainedPath(tasksRoot, journal.Final); err != nil {
		return err
	}
	if journal.Archive != "" {
		archiveRoot := journal.ArchiveRoot
		if archiveRoot == "" {
			archiveRoot = s.store.Layout().HistoryRoot()
		}
		if err := ValidateContainedPath(archiveRoot, journal.Archive); err != nil {
			return err
		}
	}
	stagingExists := pathExists(journal.Staging)
	finalExists := pathExists(journal.Final)
	archiveExists := journal.Archive != "" && pathExists(journal.Archive)
	if stagingExists && finalExists && journal.Archive == "" {
		return newTaskError("local_task_store_corrupt", "prepare journal has both staging and final directories", nil)
	}
	switch journal.Phase {
	case PreparePhasePreparing:
		if stagingExists {
			if err := os.RemoveAll(journal.Staging); err != nil {
				return newTaskError("prepare_recovery_required", "cannot remove incomplete staging directory", err)
			}
		}
		return s.removePrepareJournal(journal.ID)
	case PreparePhasePublishReady:
		if journal.Archive != "" {
			if stagingExists && finalExists && !archiveExists {
				if err := os.MkdirAll(filepath.Dir(journal.Archive), 0o700); err != nil {
					return err
				}
				if err := os.Rename(journal.Final, journal.Archive); err != nil {
					return newTaskError("prepare_recovery_required", "cannot archive previous task directory", err)
				}
				finalExists = false
				archiveExists = true
			}
			if stagingExists && !finalExists && archiveExists {
				if err := os.Rename(journal.Staging, journal.Final); err != nil {
					return newTaskError("prepare_recovery_required", "cannot publish replacement task directory", err)
				}
				stagingExists = false
				finalExists = true
			}
			if stagingExists || !finalExists || !archiveExists {
				return newTaskError("local_task_store_corrupt", "replacement prepare directories are inconsistent", nil)
			}
		}
		if !stagingExists && !finalExists {
			return newTaskError("local_task_store_corrupt", "publish-ready prepare has no directory", nil)
		}
		if stagingExists {
			if err := os.Rename(journal.Staging, journal.Final); err != nil {
				return newTaskError("prepare_recovery_required", "cannot publish prepared staging directory", err)
			}
		}
		record, err := readPreparedRecord(journal.Final)
		if err != nil {
			return err
		}
		if record.Key != journal.Key {
			return newTaskError("local_task_store_corrupt", "prepare journal task identity mismatch", nil)
		}
		if err := s.store.SaveTask(record); err != nil {
			return err
		}
		return s.removePrepareJournal(journal.ID)
	case PreparePhaseCompleted:
		return s.removePrepareJournal(journal.ID)
	default:
		return newTaskError("local_task_store_corrupt", "prepare journal phase is invalid", nil)
	}
}

func updatePreparedMetadata(store *Store, record TaskRecord, remote RemoteTaskContext) error {
	root := record.PreparationRoot
	if root == "" {
		root = store.Layout().TasksRoot()
	}
	if err := ValidateContainedPath(root, record.Directory); err != nil {
		return err
	}
	path := filepath.Join(record.Directory, "task.json")
	var metadata preparedMetadata
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &metadata) != nil {
		return newTaskError("local_task_store_corrupt", "prepared task metadata is invalid", err)
	}
	metadata.RemoteVersion = versionString(remote.RemoteTask)
	metadata.CloudMaterialDigest = remote.MaterialDigest
	metadata.Attempt = remote.Attempt
	metadata.Goal = remote.Goal
	metadata.Acceptance = remote.AcceptanceCriteria
	metadata.ReworkReason = remote.ReworkReason
	if name, source := displayNameFromRemote(remote.RemoteTask); name != "" {
		metadata.DisplayName, metadata.DisplayNameSource = name, source
	}
	return store.writeJSON(path, metadata)
}

func syncRecordDisplay(record *TaskRecord, remote RemoteTask) bool {
	if record == nil {
		return false
	}
	name, source := displayNameFromRemote(remote)
	if name == "" || (record.DisplayName == name && record.DisplayNameSource == source) {
		return false
	}
	record.DisplayName, record.DisplayNameSource = name, source
	return true
}

func materializeSources(ctx context.Context, root string, sources []MaterialSource) error {
	for _, source := range sources {
		rel, err := cleanRelativePath(source.RelativePath)
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(rel))
		if err := ValidateContainedPath(root, target); err != nil {
			return err
		}
		switch source.Kind {
		case "", "file":
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(target, []byte(source.Content), 0o600); err != nil {
				return newTaskError("prepare_failed", "cannot write file material", err)
			}
		case "git":
			if source.Repository == nil {
				return newTaskError("invalid_cloud_response", "git material has no repository context", nil)
			}
			if err := cloneExactRepository(ctx, target, *source.Repository); err != nil {
				return err
			}
		default:
			return newTaskError("provider_not_supported", "material kind is not supported", nil)
		}
	}
	return nil
}

func cloneExactRepository(ctx context.Context, target string, repo RepositoryContext) error {
	parsed, err := url.Parse(repo.CloneURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return newTaskError("unsafe_repository_url", "repository URL is invalid or contains credentials", nil)
	}
	if !gitObjectIDPattern.MatchString(repo.BaseSHA) {
		return newTaskError("invalid_cloud_response", "repository base SHA is not a full object ID", nil)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return newTaskError("prepare_failed", "cannot create repository material directory", err)
	}
	cmd := gitCommandContext(ctx, "git", "clone", "--no-checkout", "--", repo.CloneURL, target)
	cmd.Env = nonInteractiveGitEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		return gitPreparationError("repository clone", output, err)
	}
	cmd = gitCommandContext(ctx, "git", "checkout", "--detach", repo.BaseSHA)
	cmd.Dir = target
	cmd.Env = nonInteractiveGitEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		return gitPreparationError("repository checkout", output, err)
	}
	return nil
}

func nonInteractiveGitEnv() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if strings.EqualFold(name, "GIT_TERMINAL_PROMPT") || strings.EqualFold(name, "GIT_ASKPASS") {
			continue
		}
		env = append(env, value)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
}

func gitPreparationError(action string, output []byte, err error) error {
	if len(output) > maxGitDiagnosticBytes {
		output = output[:maxGitDiagnosticBytes]
	}
	diagnostic := strings.ToValidUTF8(string(output), "?")
	diagnostic = gitANSISequence.ReplaceAllString(diagnostic, "")
	diagnostic = gitURLUserInfo.ReplaceAllString(diagnostic, "$1")
	diagnostic = strings.Join(strings.Fields(diagnostic), " ")
	if diagnostic == "" {
		diagnostic = "no diagnostic output"
	}
	cause := fmt.Errorf("%s: %s: %w", action, diagnostic, err)
	return newTaskError("prepare_failed", action+" failed", cause)
}

func writePreparedFiles(store *Store, root string, metadata preparedMetadata, manifest Manifest) error {
	if err := store.writeJSON(filepath.Join(root, "task.json"), metadata); err != nil {
		return newTaskError("prepare_failed", "cannot write task metadata", err)
	}
	if err := store.writeJSON(filepath.Join(root, "manifest.json"), manifest); err != nil {
		return newTaskError("prepare_failed", "cannot write task manifest", err)
	}
	return nil
}

func readPreparedRecord(root string) (TaskRecord, error) {
	var metadata preparedMetadata
	b, err := os.ReadFile(filepath.Join(root, "task.json"))
	if err != nil || json.Unmarshal(b, &metadata) != nil {
		return TaskRecord{}, newTaskError("local_task_store_corrupt", "prepared task metadata is invalid", err)
	}
	var manifest Manifest
	b, err = os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil || json.Unmarshal(b, &manifest) != nil || manifest.SchemaVersion != SchemaVersion {
		return TaskRecord{}, newTaskError("local_task_store_corrupt", "prepared task manifest is invalid", err)
	}
	preparationRoot := metadata.PreparationRoot
	if preparationRoot == "" {
		preparationRoot = filepath.Dir(filepath.Dir(filepath.Dir(root)))
	}
	record := TaskRecord{Key: metadata.Key, DisplayName: metadata.DisplayName, DisplayNameSource: metadata.DisplayNameSource, RemoteVersion: metadata.RemoteVersion, CloudMaterialDigest: metadata.CloudMaterialDigest, Attempt: metadata.Attempt, Directory: root, PreparationRoot: preparationRoot, Prepared: true, Activity: ActivityPrepared, Manifest: &manifest}
	return record, nil
}

func (s *Service) prepareTarget(key TaskKey, workDir string) (string, string, error) {
	if workDir == "" {
		root := s.store.Layout().TasksRoot()
		return root, TaskDirIn(root, key), nil
	}
	if !filepath.IsAbs(workDir) {
		return "", "", newTaskError("invalid_workdir", "prepare working directory must be absolute", nil)
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", "", newTaskError("invalid_workdir", "cannot resolve prepare working directory", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", "", newTaskError("invalid_workdir", "prepare working directory must already exist", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", newTaskError("invalid_workdir", "cannot resolve prepare working directory links", err)
	}
	if err := ValidateContainedPath(s.store.Layout().StoreRoot(), resolved); err == nil {
		return "", "", newTaskError("invalid_workdir", "prepare working directory cannot be inside the profile store", nil)
	}
	root := filepath.Join(resolved, ".cs-cloud-tasks")
	return root, TaskDirIn(root, key), nil
}

func ensureManagedTaskRoot(root string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return newTaskError("local_task_store_unavailable", "cannot create managed task root", err)
	}
	ignore := filepath.Join(root, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(ignore, []byte("*\n!.gitignore\n"), 0o600); err != nil {
			return newTaskError("local_task_store_unavailable", "cannot protect managed task root from source control", err)
		}
	}
	return nil
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftInfo, leftStatErr := os.Stat(leftAbs)
	rightInfo, rightStatErr := os.Stat(rightAbs)
	if leftStatErr == nil && rightStatErr == nil {
		return os.SameFile(leftInfo, rightInfo)
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func (s *Service) savePrepareJournal(journal PrepareJournal) error {
	if err := validateJournalID(journal.ID); err != nil {
		return err
	}
	if err := os.MkdirAll(s.store.Layout().PrepareJournalsRoot(), 0o700); err != nil {
		return newTaskError("local_task_store_unavailable", "cannot create prepare journal directory", err)
	}
	return s.store.writeJSON(filepath.Join(s.store.Layout().PrepareJournalsRoot(), journal.ID+".json"), journal)
}

func (s *Service) removePrepareJournal(id string) error {
	if err := validateJournalID(id); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(s.store.Layout().PrepareJournalsRoot(), id+".json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return newTaskError("local_task_store_unavailable", "cannot remove prepare journal", err)
	}
	return nil
}

func validateJournalID(id string) error {
	if !taskKeyPartPattern.MatchString(id) {
		return newTaskError("local_task_store_corrupt", "journal identity is invalid", nil)
	}
	return nil
}

func providersSupported(providers []string) bool {
	for _, provider := range providers {
		if !strings.EqualFold(provider, "gitea") {
			return false
		}
	}
	return true
}

func prepareSources(remote RemoteTaskContext, key TaskKey) ([]MaterialSource, error) {
	sources := make([]MaterialSource, 0, len(remote.Materials)+len(remote.Repositories)+len(remote.RequiredDeliverables)+len(remote.OptionalDeliverables)+len(remote.PredecessorResults)+len(remote.PreviousResults))
	for _, source := range remote.Materials {
		if source.Identity == "" {
			source.Identity = source.ID
		}
		if source.SourceVersion == "" {
			source.SourceVersion = source.Version
		}
		if source.RelativePath == "" {
			return nil, newTaskError("material_content_unavailable", "cloud material has no local content location", nil)
		}
		sources = append(sources, source)
	}
	if key.Role == RoleWorker {
		deliverables := append(append([]RemoteDeliverable{}, remote.RequiredDeliverables...), remote.OptionalDeliverables...)
		for _, deliverable := range deliverables {
			sources = append(sources, deliverableSource(deliverable, "output/deliverables", MaterialOutputWritable))
		}
	}
	for _, deliverable := range remote.PredecessorResults {
		sources = append(sources, deliverableSource(deliverable, "input/predecessors", MaterialReferenceOnly))
	}
	for _, deliverable := range remote.PreviousResults {
		sources = append(sources, deliverableSource(deliverable, "input/previous-results", MaterialReferenceOnly))
	}
	for _, repository := range remote.Repositories {
		if repository.Provider != "" && !strings.EqualFold(repository.Provider, "gitea") {
			return nil, newTaskError("provider_not_supported", "repository provider is not supported", nil)
		}
		if repository.CloneURL == "" {
			return nil, newTaskError("repository_access_unavailable", "cloud context does not include a secure repository clone capability", nil)
		}
		if repository.TargetRef == "" {
			repository.TargetRef = fmt.Sprintf("refs/heads/member-tasks/%s-%s-%d", key.NodeRunID, key.Role, remote.Attempt)
		}
		if repository.BeforeSHA == "" {
			repository.BeforeSHA = repository.BaseSHA
		}
		role := MaterialOutputWritable
		if key.Role == RoleCritic {
			role = MaterialReferenceOnly
		}
		sources = append(sources, MaterialSource{Identity: repository.Identity, SourceURL: repository.SourceURL, Kind: "git", SourceVersion: repository.BaseSHA, RelativePath: "repositories/" + safeMaterialName(repository.Identity), Role: role, Repository: &repository})
	}
	return sources, nil
}

func deliverableSource(deliverable RemoteDeliverable, root string, role MaterialRole) MaterialSource {
	return MaterialSource{Identity: deliverable.ID, Title: deliverable.Title, SourceURL: deliverable.URL, Kind: "file", SourceVersion: "1", RelativePath: root + "/" + safeMaterialName(deliverable.ID) + ".md", Role: role, Content: deliverable.Content}
}

func safeMaterialName(raw string) string {
	raw = strings.ReplaceAll(raw, "/", "-")
	if taskKeyPartPattern.MatchString(raw) && raw != "." && raw != ".." {
		return raw
	}
	return digestJSON(raw)[:16]
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
