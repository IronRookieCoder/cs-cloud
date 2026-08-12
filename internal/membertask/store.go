package membertask

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type MaterialRole string

const (
	MaterialInputProtected MaterialRole = "input_protected"
	MaterialReferenceOnly  MaterialRole = "reference_only"
	MaterialOutputWritable MaterialRole = "output_writable"
)

type TaskRecord struct {
	Key                    TaskKey           `json:"key"`
	DisplayName            string            `json:"display_name,omitempty"`
	DisplayNameSource      DisplayNameSource `json:"display_name_source,omitempty"`
	RemoteVersion          string            `json:"remote_version,omitempty"`
	CloudMaterialDigest    string            `json:"cloud_material_digest,omitempty"`
	Attempt                int               `json:"attempt,omitempty"`
	Directory              string            `json:"directory,omitempty"`
	PreparationRoot        string            `json:"preparation_root,omitempty"`
	Prepared               bool              `json:"prepared"`
	Activity               Activity          `json:"activity,omitempty"`
	Dirty                  bool              `json:"dirty"`
	DirtyPaths             []string          `json:"dirty_paths,omitempty"`
	LastVerifiedAt         *time.Time        `json:"last_verified_at,omitempty"`
	Offline                bool              `json:"offline"`
	CloudStateUnverified   bool              `json:"cloud_state_unverified"`
	ReadOnly               bool              `json:"read_only"`
	WriteAuthorityLost     bool              `json:"write_authority_lost"`
	WonByOtherOperation    bool              `json:"won_by_other_operation"`
	CleanupPending         bool              `json:"cleanup_pending"`
	ReprepareRequired      bool              `json:"reprepare_required"`
	ReconfirmationRequired bool              `json:"reconfirmation_required"`
	SyncPending            bool              `json:"sync_pending"`
	Ended                  bool              `json:"ended"`
	Manifest               *Manifest         `json:"manifest,omitempty"`
	Preview                *Preview          `json:"preview,omitempty"`
	Operation              *Operation        `json:"operation,omitempty"`
	DeletePreview          *DeletePreview    `json:"delete_preview,omitempty"`
}

type Index struct {
	SchemaVersion string                `json:"schema_version"`
	Revision      uint64                `json:"revision"`
	Tasks         map[string]TaskRecord `json:"tasks"`
}

type PreparePhase string

const (
	PreparePhasePreparing    PreparePhase = "preparing"
	PreparePhasePublishReady PreparePhase = "publish_ready"
	PreparePhaseCompleted    PreparePhase = "completed"
)

type PrepareJournal struct {
	ID          string       `json:"id"`
	Key         TaskKey      `json:"key"`
	Phase       PreparePhase `json:"phase"`
	Staging     string       `json:"staging"`
	Final       string       `json:"final"`
	Root        string       `json:"root,omitempty"`
	Archive     string       `json:"archive,omitempty"`
	ArchiveTemp string       `json:"archive_temp,omitempty"`
	ArchiveRoot string       `json:"archive_root,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
}

type DeletePhase string

const (
	DeletePhaseDeleting    DeletePhase = "deleting"
	DeletePhaseQuarantined DeletePhase = "quarantined"
	DeletePhaseUnindexed   DeletePhase = "unindexed"
	DeletePhaseCompleted   DeletePhase = "completed"
)

type DeleteJournal struct {
	ID             string      `json:"id"`
	Key            TaskKey     `json:"key"`
	Phase          DeletePhase `json:"phase"`
	Original       string      `json:"original"`
	OriginalRoot   string      `json:"original_root,omitempty"`
	Quarantine     string      `json:"quarantine"`
	QuarantineRoot string      `json:"quarantine_root,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
}

type Store struct {
	layout  Layout
	replace replaceFunc
	mu      *sync.Mutex
}

var storeIndexLocks sync.Map

func OpenStore(profileRoot string) (*Store, error) {
	if profileRoot == "" {
		return nil, newTaskError("local_task_store_unavailable", "profile root is empty", nil)
	}
	layout := Layout{ProfileRoot: profileRoot}
	if err := os.MkdirAll(layout.StoreRoot(), 0o700); err != nil {
		return nil, newTaskError("local_task_store_unavailable", "cannot create task store", err)
	}
	indexPath, err := filepath.Abs(filepath.Join(layout.StoreRoot(), "index.json"))
	if err != nil {
		return nil, newTaskError("local_task_store_unavailable", "cannot resolve task store index", err)
	}
	lock, _ := storeIndexLocks.LoadOrStore(filepath.Clean(indexPath), &sync.Mutex{})
	return &Store{layout: layout, replace: atomicReplace, mu: lock.(*sync.Mutex)}, nil
}

func (s *Store) Layout() Layout    { return s.layout }
func (s *Store) indexPath() string { return filepath.Join(s.layout.StoreRoot(), "index.json") }

func (s *Store) Load() (Index, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked()
}

func (s *Store) loadUnlocked() (Index, error) {
	b, err := os.ReadFile(s.indexPath())
	if errors.Is(err, os.ErrNotExist) {
		return Index{SchemaVersion: SchemaVersion, Tasks: map[string]TaskRecord{}}, nil
	}
	if err != nil {
		return Index{}, newTaskError("local_task_store_unavailable", "cannot read task store", err)
	}
	var index Index
	if err := json.Unmarshal(b, &index); err != nil || index.SchemaVersion != SchemaVersion || index.Tasks == nil {
		return Index{}, newTaskError("local_task_store_corrupt", "task store index is invalid", err)
	}
	for raw, record := range index.Tasks {
		key, err := ParseTaskKey(raw)
		if err != nil || key != record.Key {
			return Index{}, newTaskError("local_task_store_corrupt", "task store identity mismatch", err)
		}
	}
	return index, nil
}

func (s *Store) Save(index Index) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveUnlocked(index)
}

func (s *Store) saveUnlocked(index Index) error {
	if index.SchemaVersion == "" {
		index.SchemaVersion = SchemaVersion
	}
	if index.SchemaVersion != SchemaVersion {
		return newTaskError("local_task_store_corrupt", "unsupported task store schema", nil)
	}
	if index.Tasks == nil {
		index.Tasks = map[string]TaskRecord{}
	}
	for raw, record := range index.Tasks {
		key, err := ParseTaskKey(raw)
		if err != nil || key != record.Key {
			return newTaskError("invalid_task_key", "task store record identity is invalid", err)
		}
	}
	b, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return newTaskError("local_task_store_unavailable", "cannot encode task store", err)
	}
	if err := atomicWriteFile(s.indexPath(), append(b, '\n'), s.replace); err != nil {
		return newTaskError("local_task_store_unavailable", "cannot replace task store", err)
	}
	return nil
}

func (s *Store) SaveTask(record TaskRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, err := ParseTaskKey(record.Key.String())
	if err != nil || key != record.Key {
		return newTaskError("invalid_task_key", "task record identity is invalid", err)
	}
	index, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	index.Revision++
	index.Tasks[record.Key.String()] = record
	return s.saveUnlocked(index)
}

func (s *Store) LoadTask(key TaskKey) (TaskRecord, bool, error) {
	index, err := s.Load()
	if err != nil {
		return TaskRecord{}, false, err
	}
	record, ok := index.Tasks[key.String()]
	return record, ok, nil
}

func (s *Store) RemoveTask(key TaskKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	delete(index.Tasks, key.String())
	index.Revision++
	return s.saveUnlocked(index)
}

func (s *Store) List() ([]TaskRecord, error) {
	index, err := s.Load()
	if err != nil {
		return nil, err
	}
	out := make([]TaskRecord, 0, len(index.Tasks))
	for _, record := range index.Tasks {
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.String() < out[j].Key.String() })
	return out, nil
}

func (s *Store) writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, append(b, '\n'), s.replace)
}

func (s *Store) readJSON(path string, value any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, value)
}
