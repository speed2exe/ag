package a2aserver

import (
	"context"
	"encoding/base64"
	stdjson "encoding/json"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

const (
	defaultListPageSize      = 50
	maxListPageSize          = 100
	defaultListHistoryLength = 100
	maxPushConfigsPerTask    = 50
)

// FileTaskStore is a disk-backed implementation of taskstore.Store that persists
// tasks atomically as JSON files and survives server restarts.
type FileTaskStore struct {
	dir           string
	requireAuth   bool
	authenticator taskstore.Authenticator
	timeProvider  func() time.Time

	mu    sync.RWMutex
	tasks map[a2a.TaskID]*persistedTask
}

type persistedTask struct {
	Task        *a2a.Task             `json:"task"`
	Version     taskstore.TaskVersion `json:"version"`
	User        string                `json:"user"`
	LastUpdated time.Time             `json:"lastUpdated"`
}

// FileTaskStoreConfig configures FileTaskStore.
type FileTaskStoreConfig struct {
	RequireAuth   bool
	Authenticator taskstore.Authenticator
	TimeProvider  func() time.Time
}

var _ taskstore.Store = (*FileTaskStore)(nil)

// NewFileTaskStore loads any existing tasks from dir and returns a persistent store.
func NewFileTaskStore(dir string, cfg *FileTaskStoreConfig) (*FileTaskStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create task store directory %s: %w", dir, err)
	}
	s := &FileTaskStore{
		dir:           dir,
		authenticator: a2asrv.NewTaskStoreAuthenticator(),
		timeProvider:  func() time.Time { return time.Now().UTC() },
		tasks:         make(map[a2a.TaskID]*persistedTask),
	}
	if cfg != nil {
		s.requireAuth = cfg.RequireAuth
		if cfg.Authenticator != nil {
			s.authenticator = cfg.Authenticator
		}
		if cfg.TimeProvider != nil {
			s.timeProvider = cfg.TimeProvider
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read task store directory %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("failed to read task file %s: %w", entry.Name(), err)
		}
		var rec persistedTask
		if err := stdjson.Unmarshal(data, &rec); err != nil {
			return nil, fmt.Errorf("failed to unmarshal task file %s: %w", entry.Name(), err)
		}
		if rec.Task == nil || rec.Task.ID == "" {
			continue
		}
		s.tasks[rec.Task.ID] = &rec
	}
	return s, nil
}

func (s *FileTaskStore) taskFilePath(id a2a.TaskID) string {
	return filepath.Join(s.dir, encodeKey(string(id))+".json")
}

func (s *FileTaskStore) resolveUser(ctx context.Context) (string, error) {
	userName, err := s.authenticator(ctx)
	if err != nil {
		if !s.requireAuth && errors.Is(err, a2a.ErrUnauthenticated) {
			return "", nil
		}
		return "", fmt.Errorf("taskstore auth failed: %w", err)
	}
	return userName, nil
}

// Create implements taskstore.Store.
func (s *FileTaskStore) Create(ctx context.Context, task *a2a.Task) (taskstore.TaskVersion, error) {
	if task == nil || task.ID == "" {
		return taskstore.TaskVersionMissing, fmt.Errorf("task ID is required: %w", a2a.ErrInvalidParams)
	}
	userName, err := s.resolveUser(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	taskCopy, err := deepCopyA2A(task)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("task copy failed: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tasks[task.ID]; exists {
		return taskstore.TaskVersionMissing, taskstore.ErrTaskAlreadyExists
	}

	rec := &persistedTask{
		Task:        taskCopy,
		Version:     taskstore.TaskVersion(1),
		User:        userName,
		LastUpdated: s.timeProvider(),
	}
	if err := writeStdJSONAtomic(s.taskFilePath(task.ID), rec); err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("failed to persist task %s: %w", task.ID, err)
	}
	s.tasks[task.ID] = rec
	return rec.Version, nil
}

// Update implements taskstore.Store.
func (s *FileTaskStore) Update(ctx context.Context, req *taskstore.UpdateRequest) (taskstore.TaskVersion, error) {
	if req == nil || req.Task == nil || req.Task.ID == "" {
		return taskstore.TaskVersionMissing, fmt.Errorf("task ID is required: %w", a2a.ErrInvalidParams)
	}
	userName, err := s.resolveUser(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	taskCopy, err := deepCopyA2A(req.Task)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("task copy failed: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stored := s.tasks[req.Task.ID]
	if stored == nil || stored.User != userName {
		// Mask ownership mismatch as ErrTaskNotFound per A2A spec §3.3.2.
		return taskstore.TaskVersionMissing, a2a.ErrTaskNotFound
	}
	if req.PrevVersion != taskstore.TaskVersionMissing && stored.Version != req.PrevVersion {
		return taskstore.TaskVersionMissing, taskstore.ErrConcurrentModification
	}

	rec := &persistedTask{
		Task:        taskCopy,
		Version:     stored.Version + 1,
		User:        stored.User,
		LastUpdated: s.timeProvider(),
	}
	if err := writeStdJSONAtomic(s.taskFilePath(req.Task.ID), rec); err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("failed to persist task %s: %w", req.Task.ID, err)
	}
	s.tasks[req.Task.ID] = rec
	return rec.Version, nil
}

// Get implements taskstore.Store.
func (s *FileTaskStore) Get(ctx context.Context, taskID a2a.TaskID) (*taskstore.StoredTask, error) {
	s.mu.RLock()
	stored, ok := s.tasks[taskID]
	s.mu.RUnlock()

	if !ok {
		return nil, a2a.ErrTaskNotFound
	}

	userName, err := s.resolveUser(ctx)
	if err != nil {
		return nil, err
	}
	if stored.User != userName {
		return nil, a2a.ErrTaskNotFound
	}

	taskCopy, err := deepCopyA2A(stored.Task)
	if err != nil {
		return nil, fmt.Errorf("task copy failed: %w", err)
	}
	return &taskstore.StoredTask{
		Task:    taskCopy,
		Version: stored.Version,
		User:    stored.User,
	}, nil
}

// List implements taskstore.Store.
func (s *FileTaskStore) List(ctx context.Context, req *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	if req == nil {
		req = &a2a.ListTasksRequest{}
	}
	userName, err := s.resolveUser(ctx)
	if err != nil {
		return nil, err
	}
	if s.requireAuth && userName == "" {
		return nil, a2a.ErrUnauthenticated
	}

	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = defaultListPageSize
	} else if pageSize < 1 || pageSize > maxListPageSize {
		return nil, fmt.Errorf("page size must be between 1 and %d inclusive, got %d: %w", maxListPageSize, pageSize, a2a.ErrInvalidRequest)
	}

	s.mu.RLock()
	filtered := make([]*persistedTask, 0, len(s.tasks))
	for _, rec := range s.tasks {
		if rec.User != userName {
			continue
		}
		if req.ContextID != "" && rec.Task.ContextID != req.ContextID {
			continue
		}
		if req.Status != a2a.TaskStateUnspecified && rec.Task.Status.State != req.Status {
			continue
		}
		if req.StatusTimestampAfter != nil {
			ts := rec.Task.Status.Timestamp
			if ts == nil {
				ts = &rec.LastUpdated
			}
			if ts.Before(*req.StatusTimestampAfter) {
				continue
			}
		}
		filtered = append(filtered, rec)
	}
	s.mu.RUnlock()

	totalSize := len(filtered)
	slices.SortFunc(filtered, func(a, b *persistedTask) int {
		if cmp := b.LastUpdated.Compare(a.LastUpdated); cmp != 0 {
			return cmp
		}
		return strings.Compare(string(b.Task.ID), string(a.Task.ID))
	})

	page := filtered
	if req.PageToken != "" {
		cursorTime, cursorID, err := decodePageToken(req.PageToken)
		if err != nil {
			return nil, err
		}
		startIdx := sort.Search(len(filtered), func(i int) bool {
			rec := filtered[i]
			if cmp := rec.LastUpdated.Compare(cursorTime); cmp != 0 {
				return cmp < 0
			}
			return strings.Compare(string(rec.Task.ID), string(cursorID)) < 0
		})
		page = filtered[startIdx:]
	}

	var nextPageToken string
	if len(page) > pageSize {
		last := page[pageSize-1]
		nextPageToken = encodePageToken(last.LastUpdated, last.Task.ID)
		page = page[:pageSize]
	}

	result := make([]*a2a.Task, 0, len(page))
	for _, rec := range page {
		taskCopy, err := deepCopyA2A(rec.Task)
		if err != nil {
			return nil, err
		}
		historyLen := defaultListHistoryLength
		if req.HistoryLength != nil {
			historyLen = *req.HistoryLength
		}
		if historyLen <= 0 {
			taskCopy.History = []*a2a.Message{}
		} else if len(taskCopy.History) > historyLen {
			taskCopy.History = taskCopy.History[len(taskCopy.History)-historyLen:]
		}
		if !req.IncludeArtifacts {
			taskCopy.Artifacts = nil
		}
		result = append(result, taskCopy)
	}

	return &a2a.ListTasksResponse{
		Tasks:         result,
		TotalSize:     totalSize,
		PageSize:      pageSize,
		NextPageToken: nextPageToken,
	}, nil
}

func encodePageToken(updated time.Time, taskID a2a.TaskID) string {
	raw := fmt.Appendf(nil, "%s_%s", updated.UTC().Format(time.RFC3339Nano), taskID)
	return base64.URLEncoding.EncodeToString(raw)
}

func decodePageToken(token string) (time.Time, a2a.TaskID, error) {
	decoded, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, "", a2a.ErrParseError
	}
	timePart, idPart, ok := strings.Cut(string(decoded), "_")
	if !ok || idPart == "" {
		return time.Time{}, "", a2a.ErrParseError
	}
	ts, err := time.Parse(time.RFC3339Nano, timePart)
	if err != nil {
		return time.Time{}, "", a2a.ErrParseError
	}
	return ts, a2a.TaskID(idPart), nil
}

// FilePushConfigStore is a disk-backed implementation of push.ConfigStore.
type FilePushConfigStore struct {
	dir     string
	mu      sync.RWMutex
	configs map[a2a.TaskID]map[string]*a2a.PushConfig
}

var _ push.ConfigStore = (*FilePushConfigStore)(nil)

// NewFilePushConfigStore loads persisted push notification configs from dir.
func NewFilePushConfigStore(dir string) (*FilePushConfigStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create push config store directory %s: %w", dir, err)
	}
	s := &FilePushConfigStore{
		dir:     dir,
		configs: make(map[a2a.TaskID]map[string]*a2a.PushConfig),
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read push config store directory %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("failed to read push config file %s: %w", entry.Name(), err)
		}
		var taskConfigs map[string]*a2a.PushConfig
		if err := stdjson.Unmarshal(data, &taskConfigs); err != nil {
			return nil, fmt.Errorf("failed to unmarshal push config file %s: %w", entry.Name(), err)
		}
		for _, cfg := range taskConfigs {
			if cfg == nil || cfg.TaskID == "" {
				continue
			}
			s.configs[cfg.TaskID] = taskConfigs
			break
		}
	}
	return s, nil
}

func (s *FilePushConfigStore) filePath(taskID a2a.TaskID) string {
	return filepath.Join(s.dir, encodeKey(string(taskID))+".json")
}

// Save implements push.ConfigStore.
func (s *FilePushConfigStore) Save(_ context.Context, taskID a2a.TaskID, config *a2a.PushConfig) (*a2a.PushConfig, error) {
	if err := validatePushConfig(config); err != nil {
		return nil, fmt.Errorf("%w: %w", a2a.ErrInvalidParams, err)
	}

	toSave, err := deepCopyA2A(config)
	if err != nil {
		return nil, err
	}
	if toSave.ID == "" {
		toSave.ID = a2a.NewMessageID()
	}
	toSave.TaskID = taskID

	s.mu.Lock()
	defer s.mu.Unlock()

	configs := s.configs[taskID]
	if configs == nil {
		configs = make(map[string]*a2a.PushConfig)
	}
	if _, exists := configs[toSave.ID]; !exists && len(configs) >= maxPushConfigsPerTask {
		return nil, fmt.Errorf("%w: task %s already has the maximum number of push notification configs (%d)", a2a.ErrInvalidParams, taskID, maxPushConfigsPerTask)
	}

	next := make(map[string]*a2a.PushConfig, len(configs)+1)
	for k, v := range configs {
		next[k] = v
	}
	next[toSave.ID] = toSave

	if err := writeStdJSONAtomic(s.filePath(taskID), next); err != nil {
		return nil, fmt.Errorf("failed to persist push config for task %s: %w", taskID, err)
	}
	s.configs[taskID] = next
	return deepCopyA2A(toSave)
}

// Get implements push.ConfigStore.
func (s *FilePushConfigStore) Get(_ context.Context, taskID a2a.TaskID, configID string) (*a2a.PushConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if configs, ok := s.configs[taskID]; ok {
		if cfg, ok := configs[configID]; ok {
			return deepCopyA2A(cfg)
		}
	}
	return nil, push.ErrPushConfigNotFound
}

// List implements push.ConfigStore.
func (s *FilePushConfigStore) List(_ context.Context, taskID a2a.TaskID) ([]*a2a.PushConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	configs := s.configs[taskID]
	if len(configs) == 0 {
		return []*a2a.PushConfig{}, nil
	}
	ids := make([]string, 0, len(configs))
	for id := range configs {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	result := make([]*a2a.PushConfig, 0, len(ids))
	for _, id := range ids {
		cp, err := deepCopyA2A(configs[id])
		if err != nil {
			return nil, err
		}
		result = append(result, cp)
	}
	return result, nil
}

// Delete implements push.ConfigStore.
func (s *FilePushConfigStore) Delete(_ context.Context, taskID a2a.TaskID, configID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	configs := s.configs[taskID]
	if _, ok := configs[configID]; !ok {
		return nil
	}
	next := make(map[string]*a2a.PushConfig, len(configs)-1)
	for k, v := range configs {
		if k != configID {
			next[k] = v
		}
	}
	if len(next) == 0 {
		if err := os.Remove(s.filePath(taskID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		delete(s.configs, taskID)
		return nil
	}
	if err := writeStdJSONAtomic(s.filePath(taskID), next); err != nil {
		return err
	}
	s.configs[taskID] = next
	return nil
}

// DeleteAll implements push.ConfigStore.
func (s *FilePushConfigStore) DeleteAll(_ context.Context, taskID a2a.TaskID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.Remove(s.filePath(taskID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	delete(s.configs, taskID)
	return nil
}

func validatePushConfig(cfg *a2a.PushConfig) error {
	if cfg == nil {
		return errors.New("push config cannot be nil")
	}
	if cfg.URL == "" {
		return errors.New("push config endpoint cannot be empty")
	}
	if _, err := url.ParseRequestURI(cfg.URL); err != nil {
		return fmt.Errorf("invalid push config endpoint URL: %w", err)
	}
	return nil
}

// ContextMapStore persists the mapping between A2A contextIds and ag CtxStore
// context IDs on disk and provides per-context serialization locks so multi-turn
// conversations in the same contextId stay consistent across concurrent requests.
type ContextMapStore struct {
	path string

	mu       sync.RWMutex
	mapping  map[string]string
	ctxLocks map[string]*sync.Mutex
}

// NewContextMapStore loads the A2A-to-ag context mapping from path.
func NewContextMapStore(path string) (*ContextMapStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create context map directory: %w", err)
	}
	s := &ContextMapStore{
		path:     path,
		mapping:  make(map[string]string),
		ctxLocks: make(map[string]*sync.Mutex),
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("failed to read context map %s: %w", path, err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &s.mapping); err != nil {
			return nil, fmt.Errorf("failed to unmarshal context map %s: %w", path, err)
		}
	}
	return s, nil
}

// LockContext acquires a per-contextId mutex and returns an unlock function.
func (s *ContextMapStore) LockContext(contextID string) func() {
	s.mu.Lock()
	l := s.ctxLocks[contextID]
	if l == nil {
		l = &sync.Mutex{}
		s.ctxLocks[contextID] = l
	}
	s.mu.Unlock()

	l.Lock()
	return l.Unlock
}

// Get returns the latest ag CtxStore ID for the given A2A contextId, or "".
func (s *ContextMapStore) Get(contextID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mapping[contextID]
}

// Set updates and persists the latest ag CtxStore ID for an A2A contextId.
func (s *ContextMapStore) Set(contextID, agCtxID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.mapping[contextID] = agCtxID
	data, err := json.Marshal(s.mapping)
	if err != nil {
		return fmt.Errorf("failed to marshal context map: %w", err)
	}
	return writeBytesAtomic(s.path, data)
}

func encodeKey(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

func deepCopyA2A[T any](v *T) (*T, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := stdjson.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out T
	if err := stdjson.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func writeStdJSONAtomic(path string, v any) error {
	data, err := stdjson.Marshal(v)
	if err != nil {
		return err
	}
	return writeBytesAtomic(path, data)
}

func writeBytesAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
