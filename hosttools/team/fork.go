package team

import (
	"context"
	"errors"
	"fmt"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

// Fork copies terminal task history into an unused key and branches the leader
// and PerScope conversations on their next execution, using core's validated
// lazy Fork contract. Both scopes must be idle, including standby. Durable
// lineage survives restarts. Partial provider checkpoints are never fabricated.
func (thread *Thread) Fork(newKey string) (branch *Thread, resultErr error) {
	if newKey == "" {
		return nil, errors.New("team: fork key is empty")
	}
	team := thread.team
	team.mu.Lock()
	defer team.mu.Unlock()
	if team.closed {
		return nil, adaptor.ErrAgentClosed
	}
	for _, key := range []string{thread.scope, newKey} {
		if run := team.active[key]; run != nil {
			select {
			case <-run.done:
			default:
				return nil, ErrThreadBusy
			}
		}
		if waiting := team.standbys[key]; waiting != nil {
			select {
			case <-waiting.done:
			default:
				return nil, ErrThreadBusy
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	parent, err := team.config.Store.AcquireScope(ctx, thread.scope, "fork-"+randomID(), scopeTTL)
	if err != nil {
		return nil, err
	}
	defer func() {
		releaseCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if e := team.config.Store.ReleaseScope(releaseCtx, parent); e != nil {
			resultErr = errors.Join(resultErr, e)
			branch = nil
		}
	}()
	child, err := team.config.Store.AcquireScope(ctx, newKey, "fork-"+randomID(), scopeTTL)
	if err != nil {
		return nil, err
	}
	for _, member := range team.config.Members {
		if member.Remote != nil && member.Continuity == PerScope {
			releaseCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			releaseErr := team.config.Store.ReleaseScope(releaseCtx, child)
			stop()
			return nil, errors.Join(errors.New("team: A2A does not expose conversation Fork"), releaseErr)
		}
	}
	if _, targetErr := team.config.Leader.Thread(EncodeKey(newKey, "leader")).Checkpoint(ctx); targetErr == nil {
		releaseCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		releaseErr := team.config.Store.ReleaseScope(releaseCtx, child)
		stop()
		return nil, errors.Join(ErrConflict, releaseErr)
	} else if !errors.Is(targetErr, adaptor.ErrThreadNotFound) {
		releaseCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		releaseErr := team.config.Store.ReleaseScope(releaseCtx, child)
		stop()
		return nil, errors.Join(targetErr, releaseErr)
	}
	checkpoint, err := thread.Checkpoint(ctx)
	if err == nil && (checkpoint == nil || !checkpoint.Valid) {
		err = adaptor.ErrThreadNotFound
	}
	if err == nil {
		err = team.config.Store.ForkScope(ctx, parent, child)
	}
	releaseCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	err = errors.Join(err, team.config.Store.ReleaseScope(releaseCtx, child))
	stop()
	if err != nil {
		return nil, fmt.Errorf("team: fork: %w", err)
	}
	return team.Thread(newKey), nil
}

func (run *execution) leaderThread() (*adaptor.Thread, error) {
	key := EncodeKey(run.scope, "leader")
	child := run.team.config.Leader.Thread(key)
	parent, err := run.store.ForkParent(run.ctx, run.scope)
	if err != nil || parent == "" {
		return child, err
	}
	if _, err = child.Checkpoint(run.ctx); errors.Is(err, adaptor.ErrThreadNotFound) {
		return run.team.config.Leader.Thread(EncodeKey(parent, "leader")).Fork(key), nil
	}
	return child, err
}

func (run *execution) memberConversation(member Member, key string) (adaptor.Runner, error) {
	child, err := conversation(member.Runner, key)
	if err != nil || member.Continuity != PerScope {
		return child, err
	}
	parent, err := run.store.ForkParent(run.ctx, run.scope)
	if err != nil || parent == "" {
		return child, err
	}
	reader, ok := child.(adaptor.ThreadRunner)
	if !ok {
		return nil, errors.New("team: member cannot inspect fork checkpoint")
	}
	if _, err = reader.Checkpoint(run.ctx); err == nil {
		return child, nil
	} else if !errors.Is(err, adaptor.ErrThreadNotFound) {
		return nil, err
	}
	parentKey := EncodeKey(parent, "member", member.Key)
	switch runner := member.Runner.(type) {
	case *adaptor.Agent:
		parent := runner.Thread(parentKey)
		checkpoint, err := parent.Checkpoint(run.ctx)
		if errors.Is(err, adaptor.ErrThreadNotFound) {
			return child, nil
		} else if err != nil {
			return nil, err
		}
		if checkpoint == nil || !checkpoint.Valid {
			return child, nil
		}
		return &forkedMemberConversation{ThreadRunner: parent.Fork(key), parent: parent}, nil
	case *Team:
		parent := runner.Thread(parentKey)
		checkpoint, err := parent.Checkpoint(run.ctx)
		if errors.Is(err, adaptor.ErrThreadNotFound) {
			return child, nil
		} else if err != nil {
			return nil, err
		}
		if checkpoint == nil || !checkpoint.Valid {
			return child, nil
		}
		branch, err := parent.Fork(key)
		if err != nil {
			return nil, err
		}
		return &forkedMemberConversation{ThreadRunner: branch, parent: parent}, nil
	default:
		return nil, errors.New("team: PerScope member does not support Fork")
	}
}

// A lazy fork has no child checkpoint until its first healthy execution. Only
// a conversation actually branched from a healthy parent can use that parent's
// checkpoint to validate continue. Core still validates the fork at dispatch.
type forkedMemberConversation struct {
	adaptor.ThreadRunner
	parent adaptor.ThreadRunner
}

func (conversation *forkedMemberConversation) Checkpoint(ctx context.Context) (*adaptor.Checkpoint, error) {
	checkpoint, err := conversation.ThreadRunner.Checkpoint(ctx)
	if errors.Is(err, adaptor.ErrThreadNotFound) {
		return conversation.parent.Checkpoint(ctx)
	}
	return checkpoint, err
}
