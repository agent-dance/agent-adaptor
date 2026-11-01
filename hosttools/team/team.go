package team

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// Team is a local, event-driven composite Runner. Close stops the team's
// listener and executions without closing host-owned member or leader Agents.
type Team struct {
	config    Config
	members   map[string]Member
	schemas   map[string]*jsonschema.Schema
	mu        sync.Mutex
	active    map[string]*execution
	standbys  map[string]*standby
	closed    bool
	closeDone chan struct{}
	closeErr  error
	server    *http.Server
	serveDone chan struct{}
	url       string
	secret    []byte
	grantsMu  sync.Mutex
	grants    map[string]capability
	id        string
	newTimer  func(time.Duration) *wakeTimer
}

var _ adaptor.Runner = (*Team)(nil)
var _ ConversationProvider = (*Team)(nil)

// New validates the roster and opens exactly one bearer-authenticated,
// loopback-only Streamable HTTP MCP endpoint for this Team.
func New(config Config) (*Team, error) { return newTeam(config, true) }

func newTeam(config Config, listen bool) (*Team, error) {
	if config.Leader == nil {
		return nil, errors.New("team: leader is required")
	}
	limits, err := config.Limits.resolve()
	if err != nil {
		return nil, err
	}
	config.Limits = limits
	if config.Surface.Prefix == "" {
		config.Surface.Prefix = "team_"
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(config.Surface.Prefix) {
		return nil, errors.New("team: invalid tool prefix")
	}
	if config.Store == nil {
		config.Store = NewMemoryStore()
	}
	config.Members = append([]Member(nil), config.Members...)
	team := &Team{config: config, members: make(map[string]Member), schemas: make(map[string]*jsonschema.Schema), active: make(map[string]*execution), standbys: make(map[string]*standby), grants: make(map[string]capability), secret: randomBytes(), id: randomID()[:16]}
	team.newTimer = func(delay time.Duration) *wakeTimer {
		timer := time.NewTimer(delay)
		return &wakeTimer{channel: timer.C, stop: timer.Stop}
	}
	for index, member := range config.Members {
		if member.Remote != nil {
			if !nilRunner(member.Runner) {
				return nil, fmt.Errorf("team: member %q has both Runner and Remote", member.Key)
			}
			remote, err := prepareRemote(*member.Remote)
			if err != nil {
				return nil, err
			}
			if member.Output != nil {
				return nil, fmt.Errorf("team: remote %q cannot negotiate an output schema", member.Key)
			}
			member.Runner = remote
			spec := remote.spec
			member.Remote = &spec
			if spec.Policy.MaxTimeout > 0 && (member.Timeout == 0 || spec.Policy.MaxTimeout < member.Timeout) {
				member.Timeout = min(spec.Policy.MaxTimeout, 30*time.Minute)
			}
		}
		if member.Key == "" || !utf8.ValidString(member.Key) || nilRunner(member.Runner) {
			return nil, fmt.Errorf("team: member %q requires a key and local Runner", member.Key)
		}
		if _, found := team.members[member.Key]; found {
			return nil, fmt.Errorf("team: duplicate member %q", member.Key)
		}
		if member.Continuity > PerScope || member.MaxActive < 0 || member.Timeout < 0 {
			return nil, fmt.Errorf("team: invalid settings for member %q", member.Key)
		}
		if member.MaxActive == 0 {
			member.MaxActive = 1
		}
		if member.Timeout == 0 {
			member.Timeout = 30 * time.Minute
		}
		if member.Continuity == PerScope && member.Remote == nil {
			if _, err := conversation(member.Runner, EncodeKey("probe", "member", member.Key)); err != nil {
				return nil, err
			}
		}
		member.Skills = append([]string(nil), member.Skills...)
		if member.Input != nil {
			schema, copied, err := compileSchema(member.Input)
			if err != nil {
				return nil, fmt.Errorf("team: member %q input: %w", member.Key, err)
			}
			team.schemas[member.Key] = schema
			member.Input = copied
		}
		if member.Output != nil {
			_, copied, err := compileSchema(member.Output)
			if err != nil {
				return nil, fmt.Errorf("team: member %q output: %w", member.Key, err)
			}
			member.Output = copied
		}
		team.members[member.Key] = member
		team.config.Members[index] = member
	}
	team.url = "http://127.0.0.1/mcp"
	if listen {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("team: listen: %w", err)
		}
		team.url = "http://" + listener.Addr().String() + "/mcp"
		team.serveDone = make(chan struct{})
		team.server = &http.Server{Handler: team, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute}
		go func() {
			defer close(team.serveDone)
			err := team.server.Serve(listener)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				team.mu.Lock()
				team.closed = true
				team.closeErr = errors.Join(team.closeErr, fmt.Errorf("team: MCP server: %w", err))
				for _, run := range team.active {
					run.Cancel()
				}
				for _, waiting := range team.standbys {
					waiting.cancel()
				}
				team.mu.Unlock()
			}
		}()
	}
	return team, nil
}

func nilRunner(runner adaptor.Runner) bool {
	if runner == nil {
		return true
	}
	value := reflect.ValueOf(runner)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}

type noExternalSchema struct{}

func (noExternalSchema) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema references are disabled: %s", url)
}

func compileSchema(schema map[string]any) (*jsonschema.Schema, map[string]any, error) {
	copied := cloneMap(schema)
	if copied == nil {
		return nil, nil, errors.New("schema must be JSON serializable")
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(noExternalSchema{})
	if err := compiler.AddResource("https://team.invalid/schema.json", copied); err != nil {
		return nil, nil, err
	}
	compiled, err := compiler.Compile("https://team.invalid/schema.json")
	return compiled, copied, err
}

// Send starts a team execution in an anonymous, non-durable scope. Options
// configure leader executions; ctx bounds the entire team execution.
func (team *Team) Send(ctx context.Context, text string, opts ...adaptor.CallOption) adaptor.Stream {
	return team.start(ctx, randomID(), false, text, opts)
}

// Thread returns a named team conversation. The key is preserved verbatim.
func (team *Team) Thread(scope string) *Thread { return &Thread{team: team, scope: scope} }

// Conversation implements ConversationProvider for nested PerScope teams.
func (team *Team) Conversation(key string) adaptor.ThreadRunner { return team.Thread(key) }

// Close is idempotent, stops new admissions immediately, and bounds waiting by
// ctx. Cleanup continues if an individual caller's context expires.
func (team *Team) Close(ctx context.Context) error {
	team.mu.Lock()
	if team.closeDone == nil {
		team.closed = true
		team.closeDone = make(chan struct{})
		var runs []*execution
		for _, run := range team.active {
			runs = append(runs, run)
			run.Cancel()
		}
		var standbys []*standby
		for _, waiting := range team.standbys {
			standbys = append(standbys, waiting)
			waiting.cancel()
		}
		go func() {
			for _, run := range runs {
				<-run.done
			}
			for _, waiting := range standbys {
				<-waiting.done
			}
			var closeErr error
			for _, waiting := range standbys {
				waiting.mu.Lock()
				closeErr = errors.Join(closeErr, waiting.closeErr)
				waiting.mu.Unlock()
			}
			if team.server != nil {
				closeErr = errors.Join(closeErr, team.server.Close())
				<-team.serveDone
			}
			team.mu.Lock()
			team.closeErr = errors.Join(team.closeErr, closeErr)
			close(team.closeDone)
			team.mu.Unlock()
		}()
	}
	done := team.closeDone
	team.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		team.mu.Lock()
		defer team.mu.Unlock()
		return team.closeErr
	}
}

func (team *Team) start(ctx context.Context, scope string, named bool, text string, opts []adaptor.CallOption) adaptor.Stream {
	return team.startOwned(ctx, scope, named, text, opts, nil)
}

func (team *Team) startOwned(ctx context.Context, scope string, named bool, text string, opts []adaptor.CallOption, owner *standby) adaptor.Stream {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		team.mu.Lock()
		if team.closed {
			team.mu.Unlock()
			return failedStream(scope, adaptor.ErrAgentClosed)
		}
		if waiting := team.standbys[scope]; waiting != nil && waiting.ctx.Err() != nil {
			select {
			case <-waiting.done:
				delete(team.standbys, scope)
			default:
				team.mu.Unlock()
				return failedStream(scope, ErrThreadBusy)
			}
		}
		if waiting := team.standbys[scope]; waiting != nil {
			if owner != waiting {
				team.mu.Unlock()
				return failedStream(scope, adaptor.ErrThreadBusy)
			}
			select {
			case <-waiting.ready:
			default:
				team.mu.Unlock()
				select {
				case <-waiting.ready:
					continue
				case <-ctx.Done():
					return failedStream(scope, ctx.Err())
				}
			}
		}
		if current := team.active[scope]; current != nil {
			select {
			case <-current.done:
				delete(team.active, scope)
				team.mu.Unlock()
				continue
			default:
			}
			team.mu.Unlock()
			if owner == nil {
				return failedStream(scope, adaptor.ErrThreadBusy)
			}
			messageOpts, allMessages := asMessageOptions(opts)
			if !allMessages {
				return failedStream(scope, ErrThreadBusy)
			}
			err := current.Send(ctx, text, messageOpts...)
			if errors.Is(err, adaptor.ErrRunFinished) {
				select {
				case <-current.done:
				case <-ctx.Done():
					return failedStream(scope, ctx.Err())
				}
				continue
			}
			if err != nil {
				return failedStream(scope, err)
			}
			return current
		}
		runCtx, cancel := context.WithCancel(ctx)
		run := &execution{team: team, scope: scope, id: randomID(), ctx: runCtx, cancel: cancel, commands: make(chan any, 256), done: make(chan struct{}), tasks: make(map[string]*taskRun), inflight: make(map[string]InboxMessage), warned: make(map[string]bool), senders: make(map[string]int)}
		run.log = newEventLog(run.id, scope, false)
		run.subscribed = make(chan struct{})
		if named {
			run.store = team.config.Store
		} else {
			run.store = NewMemoryStore()
		}
		team.active[scope] = run
		waiting := team.standbys[scope]
		if waiting != nil {
			waiting.mu.Lock()
			run.lease = waiting.lease
			run.sharedLease = true
			waiting.mu.Unlock()
		}
		team.mu.Unlock()
		if waiting != nil {
			waiting.attach(run)
		}
		go run.execute(text, append([]adaptor.CallOption(nil), opts...))
		return run
	}
}

func asMessageOptions(options []adaptor.CallOption) ([]adaptor.MessageOption, bool) {
	var messages []adaptor.MessageOption
	for _, option := range options {
		if option == nil {
			continue
		}
		message, ok := option.(adaptor.MessageOption)
		if !ok {
			return nil, false
		}
		messages = append(messages, message)
	}
	return messages, true
}

// Thread is the leader conversation, PerScope member conversations and task
// history belonging to one opaque host scope.
type Thread struct {
	team  *Team
	scope string
}

var _ adaptor.ThreadRunner = (*Thread)(nil)

// Key returns the host's exact scope key.
func (thread *Thread) Key() string { return thread.scope }

// Send starts a task under the scope lease. An active execution or live
// standby returns a failed Stream with adaptor.ErrThreadBusy. Supplements
// belong on the returned Stream; standby messages belong on Standby.Send.
func (thread *Thread) Send(ctx context.Context, text string, opts ...adaptor.CallOption) adaptor.Stream {
	return thread.team.start(ctx, thread.scope, true, text, opts)
}

// Checkpoint returns the leader's persisted resume checkpoint.
func (thread *Thread) Checkpoint(ctx context.Context) (*adaptor.Checkpoint, error) {
	return thread.team.config.Leader.Thread(EncodeKey(thread.scope, "leader")).Checkpoint(ctx)
}

type failedRun struct {
	log    *eventLog
	result *adaptor.Result
	err    error
	id     string
}

func failedStream(scope string, err error) adaptor.Stream {
	id := randomID()
	run := &failedRun{id: id, result: &adaptor.Result{RunID: id}, err: err, log: newEventLog(id, scope, false)}
	run.log.push(adaptor.Done{Result: run.result, Err: err})
	run.log.finish()
	return run
}
func (run *failedRun) Events() <-chan adaptor.Event     { return run.log.events() }
func (run *failedRun) RunID() string                    { return run.id }
func (run *failedRun) Cancel()                          {}
func (run *failedRun) Result() (*adaptor.Result, error) { return run.result, run.err }
func (run *failedRun) Send(_ context.Context, text string, opts ...adaptor.MessageOption) error {
	return &adaptor.MessageError{ID: messageAttributes(opts).ID, Text: text, Reason: adaptor.MessageRunFailed, Err: adaptor.ErrRunFinished}
}
