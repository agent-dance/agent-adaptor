package team

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"maps"
	"sync"
	"time"
)

// MemoryStore is a concurrency-safe TaskStore. Its zero value is ready for use.
// It retains records until the store is discarded; hosts needing retention or
// crash recovery should supply a durable TaskStore.
type MemoryStore struct {
	mu     sync.Mutex
	scopes map[string]*memoryScope
}

type memoryScope struct {
	parent       string
	lease        ScopeLease
	tasks        []Task
	messages     []InboxMessage
	messageIndex map[string]int
	artifacts    []Artifact
}

// PutArtifact stores immutable content under a live scope lease.
func (store *MemoryStore) PutArtifact(ctx context.Context, lease ScopeLease, artifact Artifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	scope, err := store.check(lease)
	if err != nil {
		return err
	}
	if artifact.Scope != lease.Scope || artifact.ID == "" || artifact.URI == "" {
		return ErrConflict
	}
	for _, current := range scope.artifacts {
		if current.ID == artifact.ID || current.URI == artifact.URI {
			if current == artifact {
				return nil
			}
			return ErrConflict
		}
	}
	scope.artifacts = append(scope.artifacts, artifact)
	return nil
}

// Artifacts lists all immutable content owned by a scope.
func (store *MemoryStore) Artifacts(ctx context.Context, key string) ([]Artifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]Artifact(nil), store.scope(key).artifacts...), nil
}

// NewMemoryStore returns an empty in-memory TaskStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func randomID() string { return hex.EncodeToString(randomBytes()) }
func randomBytes() []byte {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return value
}

func (store *MemoryStore) scope(key string) *memoryScope {
	if store.scopes == nil {
		store.scopes = make(map[string]*memoryScope)
	}
	if store.scopes[key] == nil {
		store.scopes[key] = &memoryScope{messageIndex: make(map[string]int)}
	}
	return store.scopes[key]
}

func (store *MemoryStore) check(lease ScopeLease) (*memoryScope, error) {
	scope := store.scope(lease.Scope)
	if lease.Token == "" || scope.lease.Token != lease.Token || scope.lease.Owner != lease.Owner || !scope.lease.ExpiresAt.After(time.Now()) {
		return nil, ErrConflict
	}
	return scope, nil
}

// AcquireScope obtains exclusive, expiring ownership of a scope.
func (store *MemoryStore) AcquireScope(ctx context.Context, key, owner string, ttl time.Duration) (ScopeLease, error) {
	if err := ctx.Err(); err != nil {
		return ScopeLease{}, err
	}
	if owner == "" || ttl <= 0 {
		return ScopeLease{}, ErrConflict
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	scope := store.scope(key)
	if scope.lease.Token != "" && scope.lease.ExpiresAt.After(time.Now()) {
		return ScopeLease{}, ErrThreadBusy
	}
	scope.lease = ScopeLease{Scope: key, Owner: owner, Token: randomID(), ExpiresAt: time.Now().Add(ttl)}
	for index := range scope.messages {
		message := &scope.messages[index]
		message.Deferred = message.Host && message.RequiresOptions && message.State != Delivered && message.State != DeliveryFailed
		if message.State == Delivering {
			message.State = Pending
			message.ClaimToken = ""
			message.ClaimUntil = time.Time{}
			message.MaybeDuplicate = true
		}
	}
	return scope.lease, nil
}

// RenewScope extends a live lease without changing its fencing token.
func (store *MemoryStore) RenewScope(ctx context.Context, lease ScopeLease, ttl time.Duration) (ScopeLease, error) {
	if err := ctx.Err(); err != nil {
		return ScopeLease{}, err
	}
	if ttl <= 0 {
		return ScopeLease{}, ErrConflict
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	scope, err := store.check(lease)
	if err != nil {
		return ScopeLease{}, err
	}
	scope.lease.ExpiresAt = time.Now().Add(ttl)
	for index := range scope.messages {
		if scope.messages[index].State == Delivering && scope.messages[index].ClaimUntil.After(time.Now()) {
			scope.messages[index].ClaimUntil = scope.lease.ExpiresAt
		}
	}
	return scope.lease, nil
}

// ReleaseScope releases only the matching live lease.
func (store *MemoryStore) ReleaseScope(ctx context.Context, lease ScopeLease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	scope, err := store.check(lease)
	if err != nil {
		return err
	}
	scope.lease = ScopeLease{}
	return nil
}

// Tasks returns detached task snapshots in creation order.
func (store *MemoryStore) Tasks(ctx context.Context, key string) ([]Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	var tasks []Task
	for _, task := range store.scope(key).tasks {
		tasks = append(tasks, cloneTask(task))
	}
	return tasks, nil
}

// SaveTask creates or atomically replaces one task by expected revision.
func (store *MemoryStore) SaveTask(ctx context.Context, lease ScopeLease, task Task, notifications ...InboxMessage) (Task, error) {
	if err := ctx.Err(); err != nil {
		return Task{}, err
	}
	if task.RemoteSnapshot != nil {
		if _, err := json.Marshal(task.RemoteSnapshot); err != nil {
			return Task{}, fmt.Errorf("team: remote task is not JSON: %w", err)
		}
	}
	if task.Input != nil {
		if _, err := json.Marshal(task.Input); err != nil {
			return Task{}, fmt.Errorf("team: task input is not JSON: %w", err)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	scope, err := store.check(lease)
	if err != nil {
		return Task{}, err
	}
	if task.Scope != lease.Scope || task.ID == "" {
		return Task{}, ErrConflict
	}
	messages, err := withMessages(scope.messages, scope.messageIndex, lease.Scope, notifications)
	if err != nil {
		return Task{}, err
	}
	for index, current := range scope.tasks {
		if current.ID != task.ID {
			continue
		}
		if current.Revision != task.Revision || !validTransition(current.State, task.State) {
			return Task{}, ErrConflict
		}
		task.Revision++
		task.UpdatedAt = time.Now().UTC()
		scope.tasks[index] = cloneTask(task)
		scope.setMessages(messages)
		return cloneTask(task), nil
	}
	if task.Revision != 0 || task.State != Queued {
		return Task{}, ErrConflict
	}
	task.Revision = 1
	task.UpdatedAt = time.Now().UTC()
	scope.tasks = append(scope.tasks, cloneTask(task))
	scope.setMessages(messages)
	return cloneTask(task), nil
}

func validTransition(from, to TaskState) bool {
	if terminal(from) {
		return from == to
	}
	if terminal(to) {
		return true
	}
	return from == to || from == Queued && to == Running || from == Running && to == WaitingApproval || from == WaitingApproval && to == Running
}

// PutMessage durably admits a notification. Delivered IDs deduplicate; an
// identical failed ID can be explicitly resubmitted. Clearing Deferred on
// re-admission activates a queued message without changing its identity.
func (store *MemoryStore) PutMessage(ctx context.Context, lease ScopeLease, message InboxMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	scope, err := store.check(lease)
	if err != nil {
		return err
	}
	messages, err := withMessages(scope.messages, scope.messageIndex, lease.Scope, []InboxMessage{message})
	if err != nil {
		return err
	}
	scope.setMessages(messages)
	return nil
}

func (scope *memoryScope) setMessages(messages []InboxMessage) {
	for index := len(scope.messages); index < len(messages); index++ {
		scope.messageIndex[messages[index].ID] = index
	}
	scope.messages = messages
}

func withMessages(existing []InboxMessage, indexes map[string]int, scope string, added []InboxMessage) ([]InboxMessage, error) {
	if len(added) == 0 {
		return existing, nil
	}
	messages := append([]InboxMessage(nil), existing...)
	registered := make(map[string]InboxMessage, len(added))
	for _, message := range added {
		if message.Scope != scope || message.ID == "" {
			return nil, ErrConflict
		}
		current, duplicate := registered[message.ID]
		if index, found := indexes[message.ID]; found {
			current = existing[index]
			duplicate = true
		}
		if duplicate {
			if current.Text != message.Text || current.Task != message.Task || current.From != message.From || current.Wake != message.Wake || current.Host != message.Host || current.Timing != message.Timing {
				return nil, ErrConflict
			}
			if current.State == DeliveryFailed {
				if index, found := indexes[message.ID]; found {
					messages[index].State = Pending
					messages[index].RequiresOptions = message.RequiresOptions
					messages[index].Reason = ""
					messages[index].ClaimToken = ""
					messages[index].ClaimUntil = time.Time{}
				}
			}
			if current.Deferred && !message.Deferred {
				if index, found := indexes[message.ID]; found {
					messages[index].Deferred = false
				}
			}
			continue
		}
		message.State = Pending
		message.ClaimToken = ""
		message.ClaimUntil = time.Time{}
		messages = append(messages, message)
		registered[message.ID] = message
	}
	return messages, nil
}

// Messages returns detached inbox snapshots in admission order.
func (store *MemoryStore) Messages(ctx context.Context, key string) ([]InboxMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]InboxMessage(nil), store.scope(key).messages...), nil
}

// ClaimMessages atomically claims pending or expired messages.
func (store *MemoryStore) ClaimMessages(ctx context.Context, lease ScopeLease, includeNotes bool, ttl time.Duration) ([]InboxMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ttl <= 0 {
		return nil, ErrConflict
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	scope, err := store.check(lease)
	if err != nil {
		return nil, err
	}
	var claimed []InboxMessage
	for index := range scope.messages {
		message := &scope.messages[index]
		if message.Deferred || !message.Wake && !includeNotes {
			continue
		}
		if message.State != Pending && !(message.State == Delivering && !message.ClaimUntil.After(time.Now())) {
			continue
		}
		if message.State == Delivering {
			message.MaybeDuplicate = true
		}
		message.State = Delivering
		message.ClaimToken = randomID()
		message.ClaimUntil = time.Now().Add(ttl)
		claimed = append(claimed, *message)
	}
	return claimed, nil
}

// RequeueMessage returns an exact live claim to pending after failed admission.
func (store *MemoryStore) RequeueMessage(ctx context.Context, lease ScopeLease, id, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	scope, err := store.check(lease)
	if err != nil {
		return err
	}
	index, found := scope.messageIndex[id]
	if !found {
		return ErrConflict
	}
	message := &scope.messages[index]
	if message.State != Delivering || message.ClaimToken != token || !message.ClaimUntil.After(time.Now()) {
		return ErrConflict
	}
	message.State = Pending
	message.ClaimToken = ""
	message.ClaimUntil = time.Time{}
	return nil
}

// FinishMessage commits an exact live claim to a terminal inbox state.
func (store *MemoryStore) FinishMessage(ctx context.Context, lease ScopeLease, id, token string, state InboxState, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	scope, err := store.check(lease)
	if err != nil {
		return err
	}
	if state != Delivered && state != DeliveryFailed {
		return ErrConflict
	}
	index, found := scope.messageIndex[id]
	if !found {
		return ErrConflict
	}
	message := &scope.messages[index]
	if message.State != Delivering || message.ClaimToken != token || !message.ClaimUntil.After(time.Now()) {
		return ErrConflict
	}
	message.State = state
	message.Reason = reason
	return nil
}

func cloneTask(task Task) Task {
	task.RemoteSnapshot = cloneRemoteTask(task.RemoteSnapshot)
	task.Input = cloneMap(task.Input)
	task.Unread = append([]string(nil), task.Unread...)
	if task.Usage != nil {
		usage := *task.Usage
		task.Usage = &usage
	}
	if task.Result != nil {
		copied := *adaptor.ComposeResults(task.Result.RunID, task.Result)
		copied.Metadata = maps.Clone(task.Result.Metadata)
		copied.Usage = sumUsage(nil, task.Result.Usage)
		task.Result = &copied
	}
	return task
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	var output map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if decoder.Decode(&output) != nil {
		return nil
	}
	return output
}

// ForkParent returns the durable parent scope, if any.
func (store *MemoryStore) ForkParent(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.scope(key).parent, nil
}

// ForkScope atomically copies terminal history and records lazy fork lineage.
func (store *MemoryStore) ForkScope(ctx context.Context, parent, child ScopeLease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	source, err := store.check(parent)
	if err != nil {
		return err
	}
	target, err := store.check(child)
	if err != nil {
		return err
	}
	if parent.Scope == child.Scope || target.parent != "" || len(target.tasks) > 0 || len(target.messages) > 0 || len(target.artifacts) > 0 {
		return ErrConflict
	}
	for _, task := range source.tasks {
		if !terminal(task.State) {
			return ErrThreadBusy
		}
	}
	for _, task := range source.tasks {
		task = cloneTask(task)
		task.Scope = child.Scope
		if task.Conversation == EncodeKey(parent.Scope, "member", task.Member) {
			task.Conversation = EncodeKey(child.Scope, "member", task.Member)
		} else {
			task.Conversation = ""
		}
		target.tasks = append(target.tasks, task)
	}
	for _, artifact := range source.artifacts {
		artifact.Scope = child.Scope
		target.artifacts = append(target.artifacts, artifact)
	}
	target.parent = parent.Scope
	return nil
}
