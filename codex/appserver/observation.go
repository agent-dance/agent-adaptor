package appserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/capabilityobs"
	"github.com/agent-dance/agent-adaptor/internal/todoobs"
	"github.com/agent-dance/agent-adaptor/todo"
)

// Hand-written projections of the checked-in official discriminated schemas.
// collab items have no canonical role; thread/started provides it, and only a
// current-turn spawn's receiver identity can associate it with a catalog key.
type collabItem struct {
	ID                string   `json:"id"`
	Type              string   `json:"type"`
	Tool              string   `json:"tool"`
	Status            string   `json:"status"`
	SenderThreadID    string   `json:"senderThreadId"`
	ReceiverThreadIDs []string `json:"receiverThreadIds"`
}
type collabObservation struct {
	item               collabItem
	startItem          collabItem
	started, completed bool
	conflict           bool
}
type childRole struct {
	role     string
	conflict bool
}
type observations struct {
	catalog     *capabilityobs.Catalog
	tracker     *capabilityobs.Tracker
	table       *todoobs.Table
	skills      []UserInput
	skillPaths  map[string]string
	children    map[string]childRole
	collab      map[string]*collabObservation
	collabOrder []string
	notices     map[string]bool
}

func newObservations(opts Options) *observations {
	entries := make([]capabilityobs.Entry, 0)
	paths := make(map[string]string)
	for _, s := range opts.ResolvedSkills {
		entries = append(entries, capabilityobs.Entry{Kind: capability.Skill, RuntimeName: s.RuntimeName, Key: s.Key})
		// Inputs were already selected by the driver; retain only exact name/path
		// pairs whose source was resolved for this run.
		path := s.SourcePath
		for _, input := range opts.SkillInputs {
			if input.Name == s.RuntimeName && input.Path != "" && (input.Path == path || input.Path == path+"/SKILL.md" || input.Path == path+`\SKILL.md`) {
				paths[input.Name] = input.Path
			}
		}
	}
	for _, server := range opts.ResolvedMCP {
		entries = append(entries, capabilityobs.Entry{Kind: capability.MCP, RuntimeName: server.Key, Key: server.Key})
	}
	for _, agent := range opts.ResolvedAgents {
		entries = append(entries, capabilityobs.Entry{Kind: capability.Subagent, RuntimeName: agent.RuntimeName, Key: agent.Key})
	}
	catalog, _ := capabilityobs.NewCatalog(entries)
	table, _ := todoobs.NewTable(todoobs.Scope{})
	return &observations{catalog: catalog, tracker: capabilityobs.NewTracker(), table: table, skills: append([]UserInput(nil), opts.SkillInputs...), skillPaths: paths, children: make(map[string]childRole), collab: make(map[string]*collabObservation), notices: make(map[string]bool)}
}

func (s *runState) observationNotice(code string) {
	if s.observation.notices[code] {
		return
	}
	s.observation.notices[code] = true
	_ = s.sink.Emit(driver.RunEvent{Type: driver.RunEventRuntime, Text: "Codex observation unavailable", Data: map[string]any{"code": code}})
}
func (s *runState) emitCapability(v *capability.Invocation, err error) {
	if err != nil {
		s.observationNotice("capability_unresolved")
		return
	}
	if v != nil {
		_ = s.sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: v})
	}
}
func (s *runState) lookupCapability(kind capability.Kind, name string) (string, bool) {
	if s.observation.catalog == nil {
		s.observationNotice("capability_unresolved")
		return "", false
	}
	key, err := s.observation.catalog.Lookup(kind, name)
	if err != nil {
		s.observationNotice("capability_unresolved")
		return "", false
	}
	return key, true
}
func (s *runState) startCapability(id string, ref capability.Ref, evidence capability.Evidence) {
	s.emitCapability(s.observation.tracker.Start(capability.Invocation{InvocationID: id, Ref: ref, Phase: capability.Started, Evidence: evidence, Source: capability.Provider, OccurredAt: time.Now().UTC()}))
}
func (s *runState) terminalCapability(id string, phase capability.Phase, code capability.ErrorCode, duration *time.Duration) {
	s.emitCapability(s.observation.tracker.Terminal(capabilityobs.Key{InvocationID: id}, phase, code, time.Now().UTC(), duration))
}

func (s *runState) acceptSkillInputs() {
	for i, input := range s.observation.skills {
		if input.Type != UserInputKindSkill || input.Path == "" || s.observation.skillPaths[input.Name] != input.Path {
			continue
		}
		key, ok := s.lookupCapability(capability.Skill, input.Name)
		if !ok {
			continue
		}
		id := observationID("skill-input", s.turnID, strconv.Itoa(i))
		s.startCapability(id, capability.Ref{Kind: capability.Skill, Key: key, Operation: "activate"}, capability.NativeInputAccepted)
		// Completion is the accepted native-input operation, not skill execution.
		s.terminalCapability(id, capability.Completed, "", nil)
	}
}

func (s *runState) observeItem(raw json.RawMessage, started bool) {
	item, err := DecodeThreadItem(raw)
	if err != nil {
		return
	}
	if item.McpToolCall != nil {
		body := item.McpToolCall
		key, ok := s.lookupCapability(capability.MCP, body.Server)
		if !ok {
			return
		}
		if started {
			if _, exists := s.observationMCP[item.ID]; !exists && len(s.observationMCP) >= 4096 {
				s.observationNotice("capability_limit")
				return
			}
			if body.Status != "inProgress" {
				s.observationNotice("capability_invalid")
				return
			}
			s.startCapability(item.ID, capability.Ref{Kind: capability.MCP, Key: key, Operation: body.Tool}, capability.ProviderProtocol)
		} else {
			phase, code := capability.Completed, capability.ErrorCode("")
			switch body.Status {
			case "completed":
				if len(body.Error) > 0 && !bytes.Equal(bytes.TrimSpace(body.Error), []byte("null")) {
					phase, code = capability.Failed, capability.ToolFailed
				}
			case "failed":
				phase, code = capability.Failed, capability.ToolFailed
			default:
				s.observationNotice("capability_invalid")
				return
			}
			var duration *time.Duration
			if body.DurationMs != nil && *body.DurationMs >= 0 && *body.DurationMs <= math.MaxInt64/int64(time.Millisecond) {
				d := time.Duration(*body.DurationMs) * time.Millisecond
				duration = &d
			}
			// A terminal cannot silently change the identity established by start.
			// Tracker.Start validates this against the stored lifecycle without emitting
			// an extra start on a valid replay; absent starts stay unobserved.
			if prior, ok := s.observationMCP[item.ID]; !ok || prior != (capability.Ref{Kind: capability.MCP, Key: key, Operation: body.Tool}) {
				s.observationNotice("capability_unresolved")
				return
			}
			s.terminalCapability(item.ID, phase, code, duration)
		}
		if started {
			if _, exists := s.observationMCP[item.ID]; !exists {
				s.observationMCP[item.ID] = capability.Ref{Kind: capability.MCP, Key: key, Operation: body.Tool}
			}
		}
		return
	}
	var c collabItem
	if json.Unmarshal(raw, &c) != nil || c.Type != "collabAgentToolCall" {
		return
	}
	if c.Tool != "spawnAgent" || c.SenderThreadID != s.threadID || c.ID == "" {
		return
	}
	if started && c.Status != "inProgress" {
		s.observationNotice("capability_invalid")
		return
	}
	o := s.observation.collab[c.ID]
	if o == nil {
		if !started || len(s.observation.collab) >= 128 {
			s.observationNotice("capability_unresolved")
			return
		}
		o = &collabObservation{}
		s.observation.collab[c.ID] = o
		s.observation.collabOrder = append(s.observation.collabOrder, c.ID)
	}
	if o.conflict {
		return
	}
	if len(o.item.ReceiverThreadIDs) > 0 && !reflect.DeepEqual(o.item.ReceiverThreadIDs, c.ReceiverThreadIDs) {
		o.conflict = true
		s.observationNotice("capability_invalid")
		return
	}
	if started {
		if o.started {
			if !reflect.DeepEqual(o.startItem, c) {
				o.conflict = true
				s.observationNotice("capability_invalid")
			}
			return
		}
		o.started = true
		o.startItem = c
	} else {
		o.completed = true
	}
	o.item = c
	s.observeCollab(o)
}
func (s *runState) observeCollab(o *collabObservation) {
	c := o.item
	if o.conflict || len(c.ReceiverThreadIDs) != 1 {
		return
	}
	child, ok := s.observation.children[c.ReceiverThreadIDs[0]]
	if !ok || child.conflict {
		return
	}
	key, ok := s.lookupCapability(capability.Subagent, child.role)
	if !ok {
		return
	}
	id := observationID("collab", c.ID)
	s.startCapability(id, capability.Ref{Kind: capability.Subagent, Key: key, Operation: "spawn"}, capability.ProviderProtocol)
	if !o.completed {
		return
	}
	switch c.Status {
	case "completed":
		s.terminalCapability(id, capability.Completed, "", nil)
	case "failed":
		s.terminalCapability(id, capability.Failed, capability.ToolFailed, nil)
	default:
		s.observationNotice("capability_invalid")
	}
}

// This narrowly recognizes subordinate metadata; it never changes the active
// thread/turn, emits that child's semantics or guesses a parent tool identity.
func (s *runState) observeChildThread(params json.RawMessage) bool {
	var body struct {
		Thread struct {
			ID        string `json:"id"`
			AgentRole string `json:"agentRole"`
			Source    struct {
				SubAgent struct {
					Spawn struct {
						Parent string `json:"parent_thread_id"`
						Role   string `json:"agent_role"`
					} `json:"thread_spawn"`
				} `json:"subAgent"`
			} `json:"source"`
		} `json:"thread"`
	}
	if json.Unmarshal(params, &body) != nil || body.Thread.ID == "" || body.Thread.ID == s.threadID || body.Thread.Source.SubAgent.Spawn.Parent != s.threadID {
		return false
	}
	role := body.Thread.AgentRole
	if role == "" {
		role = body.Thread.Source.SubAgent.Spawn.Role
	}
	if len(s.observation.children) >= 128 {
		s.observationNotice("capability_unresolved")
		return true
	}
	child := childRole{role: role}
	if old, ok := s.observation.children[body.Thread.ID]; ok && (old.conflict || old.role != role) {
		child.conflict = true
	}
	if body.Thread.Source.SubAgent.Spawn.Role != "" && body.Thread.Source.SubAgent.Spawn.Role != role {
		child.conflict = true
	}
	s.observation.children[body.Thread.ID] = child
	for _, id := range s.observation.collabOrder {
		s.observeCollab(s.observation.collab[id])
	}
	return true
}

// Plan has no provider IDs: tuple-encoded turn and position identify only a
// slot in each complete snapshot. They are explicitly synthetic, never task IDs.
func observationID(parts ...string) string {
	b, _ := json.Marshal(parts)
	return "synthetic:" + base64.RawURLEncoding.EncodeToString(b)
}
func (s *runState) observePlan(params json.RawMessage) {
	var body struct {
		Plan json.RawMessage `json:"plan"`
	}
	var steps []struct {
		Step   string `json:"step"`
		Status string `json:"status"`
	}
	if !validObservationJSON(params) || json.Unmarshal(params, &body) != nil || len(bytes.TrimSpace(body.Plan)) == 0 || bytes.TrimSpace(body.Plan)[0] != '[' || json.Unmarshal(body.Plan, &steps) != nil {
		s.observationNotice("todo_invalid")
		return
	}
	items := make([]todo.Item, 0, len(steps))
	for i, step := range steps {
		var status todo.Status
		switch step.Status {
		case "pending":
			status = todo.Pending
		case "inProgress":
			status = todo.InProgress
		case "completed":
			status = todo.Completed
		default:
			s.observationNotice("todo_invalid")
			return
		}
		items = append(items, todo.Item{ID: observationID("plan", s.turnID, strconv.Itoa(i)), Content: step.Step, Status: status, SyntheticID: true})
	}
	snapshot, err := s.observation.table.Replace(items, todo.PlanUpdate, time.Now().UTC())
	if err != nil {
		s.observationNotice("todo_invalid")
		return
	}
	if snapshot != nil {
		_ = s.sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTodoUpdated, Todo: snapshot})
	}
}
func (s *runState) closeObservations(runErr error) {
	for _, o := range s.observation.collab {
		if o.conflict || len(o.item.ReceiverThreadIDs) != 1 {
			s.observationNotice("capability_unresolved")
			continue
		}
		if child, ok := s.observation.children[o.item.ReceiverThreadIDs[0]]; !ok || child.conflict || child.role == "" {
			s.observationNotice("capability_unresolved")
		}
	}
	phase, code := capability.Interrupted, capability.RunInterrupted
	if errors.Is(runErr, context.Canceled) {
		phase, code = capability.Cancelled, capability.RunCancelled
	}
	values, _ := s.observation.tracker.Close(phase, code, time.Now().UTC())
	for i := range values {
		s.emitCapability(&values[i], nil)
	}
}

// encoding/json replaces unpaired escaped surrogates with U+FFFD. Reject those
// malformed protocol strings so a repaired string cannot become a plan fact.
func validObservationJSON(raw []byte) bool {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) || raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		v, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if v >= 0xDC00 && v <= 0xDFFF {
			return false
		}
		if v >= 0xD800 && v <= 0xDBFF {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			i += 6
		}
	}
	return true
}
