package a2a

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	a2ataskstore "github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

type ephemeralTaskStoreConfig struct {
	maxTasks int
	ttl      time.Duration
	auth     a2ataskstore.Authenticator
	now      func() time.Time
}

type ephemeralTaskRecord struct {
	task        *a2aproto.Task
	version     a2ataskstore.TaskVersion
	user        string
	lastUpdated time.Time
}

type ephemeralTaskStore struct {
	mu     sync.RWMutex
	tasks  map[a2aproto.TaskID]*ephemeralTaskRecord
	config ephemeralTaskStoreConfig
}

var _ a2ataskstore.Store = (*ephemeralTaskStore)(nil)

func newConfiguredTaskStore(opts TaskLifecycleOptions) (a2ataskstore.Store, error) {
	if opts.Store != nil {
		return opts.Store, nil
	}
	cfg := EphemeralTaskStoreOptions{
		MaxTasks: DefaultEphemeralTaskLimit,
		TTL:      DefaultEphemeralTaskTTL,
	}
	if opts.Ephemeral != nil {
		if opts.Ephemeral.MaxTasks < 0 {
			return nil, fmt.Errorf("a2a bridge: ephemeral task store max tasks must be >= 0")
		}
		if opts.Ephemeral.TTL < 0 {
			return nil, fmt.Errorf("a2a bridge: ephemeral task store TTL must be >= 0")
		}
		if opts.Ephemeral.MaxTasks > 0 {
			cfg.MaxTasks = opts.Ephemeral.MaxTasks
		}
		if opts.Ephemeral.TTL > 0 {
			cfg.TTL = opts.Ephemeral.TTL
		}
	}
	return newEphemeralTaskStore(ephemeralTaskStoreConfig{
		maxTasks: cfg.MaxTasks,
		ttl:      cfg.TTL,
		auth:     a2asrv.NewTaskStoreAuthenticator(),
	})
}

func newEphemeralTaskStore(cfg ephemeralTaskStoreConfig) (*ephemeralTaskStore, error) {
	if cfg.maxTasks <= 0 {
		return nil, fmt.Errorf("a2a bridge: ephemeral task store max tasks must be > 0")
	}
	if cfg.ttl <= 0 {
		return nil, fmt.Errorf("a2a bridge: ephemeral task store TTL must be > 0")
	}
	if cfg.auth == nil {
		cfg.auth = func(context.Context) (string, error) { return "", nil }
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &ephemeralTaskStore{
		tasks:  make(map[a2aproto.TaskID]*ephemeralTaskRecord),
		config: cfg,
	}, nil
}

func (s *ephemeralTaskStore) Create(ctx context.Context, task *a2aproto.Task) (a2ataskstore.TaskVersion, error) {
	if err := validateStoredTask(task); err != nil {
		return a2ataskstore.TaskVersionMissing, err
	}
	user, err := s.config.auth(ctx)
	if err != nil {
		return a2ataskstore.TaskVersionMissing, fmt.Errorf("taskstore auth failed: %w", err)
	}
	copy, err := cloneTask(task)
	if err != nil {
		return a2ataskstore.TaskVersionMissing, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(s.config.now())
	if s.tasks[task.ID] != nil {
		return a2ataskstore.TaskVersionMissing, a2ataskstore.ErrTaskAlreadyExists
	}
	version := a2ataskstore.TaskVersion(1)
	s.tasks[task.ID] = &ephemeralTaskRecord{
		task:        copy,
		version:     version,
		user:        user,
		lastUpdated: s.config.now(),
	}
	s.evictOverflowLocked()
	return version, nil
}

func (s *ephemeralTaskStore) Update(ctx context.Context, req *a2ataskstore.UpdateRequest) (a2ataskstore.TaskVersion, error) {
	if req == nil {
		return a2ataskstore.TaskVersionMissing, fmt.Errorf("missing update request: %w", a2aproto.ErrInvalidParams)
	}
	if err := validateStoredTask(req.Task); err != nil {
		return a2ataskstore.TaskVersionMissing, err
	}
	copy, err := cloneTask(req.Task)
	if err != nil {
		return a2ataskstore.TaskVersionMissing, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(s.config.now())
	stored := s.tasks[req.Task.ID]
	if stored == nil {
		return a2ataskstore.TaskVersionMissing, a2aproto.ErrTaskNotFound
	}
	if req.PrevVersion != a2ataskstore.TaskVersionMissing && stored.version != req.PrevVersion {
		return a2ataskstore.TaskVersionMissing, a2ataskstore.ErrConcurrentModification
	}
	version := stored.version + 1
	s.tasks[req.Task.ID] = &ephemeralTaskRecord{
		task:        copy,
		version:     version,
		user:        stored.user,
		lastUpdated: s.config.now(),
	}
	return version, nil
}

func (s *ephemeralTaskStore) Get(ctx context.Context, taskID a2aproto.TaskID) (*a2ataskstore.StoredTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(s.config.now())
	stored := s.tasks[taskID]
	if stored == nil {
		return nil, a2aproto.ErrTaskNotFound
	}
	copy, err := cloneTask(stored.task)
	if err != nil {
		return nil, fmt.Errorf("task copy failed: %w", err)
	}
	return &a2ataskstore.StoredTask{Task: copy, Version: stored.version}, nil
}

func (s *ephemeralTaskStore) List(ctx context.Context, req *a2aproto.ListTasksRequest) (*a2aproto.ListTasksResponse, error) {
	if req == nil {
		req = &a2aproto.ListTasksRequest{}
	}
	const defaultPageSize = 50

	user, err := s.config.auth(ctx)
	if user == "" || err != nil {
		return nil, a2aproto.ErrUnauthenticated
	}

	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = defaultPageSize
	} else if pageSize < 1 || pageSize > 100 {
		return nil, fmt.Errorf("page size must be between 1 and 100 inclusive, got %d: %w", pageSize, a2aproto.ErrInvalidRequest)
	}

	s.mu.Lock()
	s.pruneExpiredLocked(s.config.now())
	filtered := s.filterLocked(user, req)
	s.mu.Unlock()

	sort.Slice(filtered, func(i, j int) bool {
		if cmp := filtered[j].lastUpdated.Compare(filtered[i].lastUpdated); cmp != 0 {
			return cmp < 0
		}
		return strings.Compare(string(filtered[j].task.ID), string(filtered[i].task.ID)) < 0
	})

	page, nextToken, err := paginateTaskRecords(filtered, pageSize, req)
	if err != nil {
		return nil, err
	}
	tasks, err := listTaskCopies(page, req)
	if err != nil {
		return nil, err
	}

	return &a2aproto.ListTasksResponse{
		Tasks:         tasks,
		TotalSize:     len(filtered),
		PageSize:      pageSize,
		NextPageToken: nextToken,
	}, nil
}

func (s *ephemeralTaskStore) filterLocked(user string, req *a2aproto.ListTasksRequest) []*ephemeralTaskRecord {
	filtered := make([]*ephemeralTaskRecord, 0, len(s.tasks))
	for _, stored := range s.tasks {
		if stored.user != user {
			continue
		}
		if req.ContextID != "" && stored.task.ContextID != req.ContextID {
			continue
		}
		if req.Status != a2aproto.TaskStateUnspecified && stored.task.Status.State != req.Status {
			continue
		}
		if req.StatusTimestampAfter != nil && stored.task.Status.Timestamp != nil && stored.task.Status.Timestamp.Before(*req.StatusTimestampAfter) {
			continue
		}
		filtered = append(filtered, stored)
	}
	return filtered
}

func (s *ephemeralTaskStore) pruneExpiredLocked(now time.Time) {
	for id, stored := range s.tasks {
		if now.Sub(stored.lastUpdated) >= s.config.ttl {
			delete(s.tasks, id)
		}
	}
}

func (s *ephemeralTaskStore) evictOverflowLocked() {
	if len(s.tasks) <= s.config.maxTasks {
		return
	}
	records := make([]*ephemeralTaskRecord, 0, len(s.tasks))
	for _, stored := range s.tasks {
		records = append(records, stored)
	}
	sort.Slice(records, func(i, j int) bool {
		if cmp := records[i].lastUpdated.Compare(records[j].lastUpdated); cmp != 0 {
			return cmp < 0
		}
		return strings.Compare(string(records[i].task.ID), string(records[j].task.ID)) < 0
	})
	toEvict := len(records) - s.config.maxTasks
	for i := 0; i < toEvict; i++ {
		delete(s.tasks, records[i].task.ID)
	}
}

func paginateTaskRecords(records []*ephemeralTaskRecord, pageSize int, req *a2aproto.ListTasksRequest) ([]*ephemeralTaskRecord, string, error) {
	var page []*ephemeralTaskRecord
	if req.PageToken != "" {
		cursorTime, cursorTaskID, err := decodeTaskPageToken(req.PageToken)
		if err != nil {
			return nil, "", err
		}
		start := sort.Search(len(records), func(i int) bool {
			record := records[i]
			timeCmp := record.lastUpdated.Compare(cursorTime)
			if timeCmp < 0 {
				return true
			}
			if timeCmp > 0 {
				return false
			}
			return strings.Compare(string(record.task.ID), string(cursorTaskID)) < 0
		})
		page = records[start:]
	} else {
		page = records
	}

	var nextToken string
	if pageSize >= len(page) {
		pageSize = len(page)
	} else {
		last := page[pageSize-1]
		nextToken = encodeTaskPageToken(last.lastUpdated, last.task.ID)
	}
	return page[:pageSize], nextToken, nil
}

func listTaskCopies(records []*ephemeralTaskRecord, req *a2aproto.ListTasksRequest) ([]*a2aproto.Task, error) {
	const defaultHistoryLength = 100
	out := make([]*a2aproto.Task, 0, len(records))
	for _, stored := range records {
		copy, err := cloneTask(stored.task)
		if err != nil {
			return nil, err
		}
		historyLength := defaultHistoryLength
		if req.HistoryLength != nil {
			historyLength = *req.HistoryLength
		}
		if historyLength == 0 {
			copy.History = []*a2aproto.Message{}
		} else if historyLength > 0 && len(copy.History) > historyLength {
			copy.History = copy.History[len(copy.History)-historyLength:]
		}
		if !req.IncludeArtifacts {
			copy.Artifacts = nil
		}
		out = append(out, copy)
	}
	return out, nil
}

func cloneTask(task *a2aproto.Task) (*a2aproto.Task, error) {
	if task == nil {
		return nil, nil
	}
	raw, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}
	var copy a2aproto.Task
	if err := json.Unmarshal(raw, &copy); err != nil {
		return nil, err
	}
	return &copy, nil
}

func encodeTaskPageToken(updated time.Time, taskID a2aproto.TaskID) string {
	token := fmt.Sprintf("%s_%s", updated.Format(time.RFC3339Nano), taskID)
	return base64.URLEncoding.EncodeToString([]byte(token))
}

func decodeTaskPageToken(token string) (time.Time, a2aproto.TaskID, error) {
	raw, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, "", a2aproto.ErrParseError
	}
	parts := strings.Split(string(raw), "_")
	if len(parts) != 2 {
		return time.Time{}, "", a2aproto.ErrParseError
	}
	updated, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", a2aproto.ErrParseError
	}
	return updated, a2aproto.TaskID(parts[1]), nil
}

func validateStoredTask(task *a2aproto.Task) error {
	if task == nil {
		return nil
	}
	if err := validateStoredMessage(task.Status.Message); err != nil {
		return err
	}
	for _, msg := range task.History {
		if err := validateStoredMessage(msg); err != nil {
			return err
		}
	}
	for _, artifact := range task.Artifacts {
		if err := validateStoredArtifact(artifact); err != nil {
			return err
		}
	}
	return validateMetadata(task.Metadata)
}

func validateStoredArtifact(artifact *a2aproto.Artifact) error {
	if artifact == nil {
		return nil
	}
	if err := validateParts(artifact.Parts); err != nil {
		return err
	}
	return validateMetadata(artifact.Metadata)
}

func validateStoredMessage(msg *a2aproto.Message) error {
	if msg == nil {
		return nil
	}
	if err := validateParts(msg.Parts); err != nil {
		return err
	}
	return validateMetadata(msg.Metadata)
}

func validateParts(parts a2aproto.ContentParts) error {
	for _, part := range parts {
		if part == nil {
			continue
		}
		if err := validateMetadata(part.Metadata); err != nil {
			return err
		}
	}
	return nil
}

func validateMetadata(meta map[string]any) error {
	return validateMetadataValue(meta, map[string]struct{}{})
}

func validateMetadataValue(value any, seen map[string]struct{}) error {
	if value == nil {
		return nil
	}

	switch value.(type) {
	case bool, int, int8, int16, int32, int64, float32, float64, string:
		return nil
	}

	key := fmt.Sprintf("%p", value)
	if _, ok := seen[key]; ok {
		return fmt.Errorf("circular reference in Metadata")
	}
	seen[key] = struct{}{}
	defer delete(seen, key)

	if items, ok := value.([]any); ok {
		for _, item := range items {
			if err := validateMetadataValue(item, seen); err != nil {
				return err
			}
		}
		return nil
	}
	if fields, ok := value.(map[string]any); ok {
		for _, item := range fields {
			if err := validateMetadataValue(item, seen); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("%T is not permitted in Metadata, must be one of nil, bool, int, float, string, []any, map[string]any", value)
}
