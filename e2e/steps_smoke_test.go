//go:build e2e

package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/cucumber/godog"
)

func initializeScenario(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		executedScenarios.Add(1)
		world, err := newScenarioWorld()
		if err != nil {
			return ctx, err
		}
		return context.WithValue(ctx, worldContextKey{}, world), nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		world, err := worldFrom(ctx)
		if err != nil {
			return ctx, err
		}
		return ctx, world.closeAndVerify(ctx)
	})

	registerSmokeSetupSteps(sc)
	registerSmokeActionSteps(sc)
	registerSmokeAssertionSteps(sc)
}

func registerSmokeSetupSteps(sc *godog.ScenarioContext) {
	sc.Step(`^"([^"]+)" 使用默认常驻进程模式并具有稳定 Thread key$`, func(ctx context.Context, provider string) error {
		world, err := worldFrom(ctx)
		if err != nil {
			return err
		}
		if err := world.selectProvider(provider); err != nil {
			return err
		}
		world.spawn = false
		return world.resolveRealCLI(ctx)
	})
	sc.Step(`^本机真实 (Claude Code|CodeBuddy|Codex) 已认证$`, func(ctx context.Context, displayName string) error {
		world, err := worldFrom(ctx)
		if err != nil {
			return err
		}
		provider, err := providerFromDisplayName(displayName)
		if err != nil {
			return err
		}
		if err := world.selectProvider(string(provider)); err != nil {
			return err
		}
		return world.resolveRealCLI(ctx)
	})
	sc.Step(`^Agent 使用临时 workspace、native 或只读 clone profile 和 memory Thread Store$`, configureRealAgent)
	sc.Step(`^Agent 使用临时 workspace、隔离 config dir 和 memory Thread Store$`, configureRealAgent)
	sc.Step(`^Agent 使用临时 workspace、有效 CODEX_HOME 和 memory Thread Store$`, configureRealAgent)
	sc.Step(`^使用默认常驻进程模式$`, func(ctx context.Context) error {
		world, err := worldFrom(ctx)
		if err != nil {
			return err
		}
		if world.agent != nil {
			return errors.New("process mode must be selected before constructing Agent")
		}
		world.spawn = false
		return nil
	})
}

func configureRealAgent(ctx context.Context) error {
	world, err := worldFrom(ctx)
	if err != nil {
		return err
	}
	for _, path := range []string{world.root, world.workspace} {
		if path == "" || !strings.HasPrefix(filepathClean(path), filepathClean(world.root)) {
			return fmt.Errorf("writable path escaped scenario root: %s", path)
		}
	}
	return nil
}

func registerSmokeActionSteps(sc *godog.ScenarioContext) {
	sc.Step(`^turn1 要求记住随机 token$`, rememberTokenTurn)
	sc.Step(`^turn1 要求记住随机 token A 并只回复确认词$`, rememberTokenTurn)
	sc.Step(`^turn1 streaming 要求记住随机 token A$`, rememberTokenTurn)
	sc.Step(`^turn2 要求返回该 token$`, returnTokenTurn)
	sc.Step(`^turn2 要求返回 A$`, returnTokenTurn)
	sc.Step(`^turn2 streaming 要求返回 A$`, returnTokenTurn)
	sc.Step(`^turn3 要求再次返回该 token$`, returnTokenTurn)
	sc.Step(`^turn3 要求再次返回 A$`, returnTokenTurn)
	sc.Step(`^turn3 streaming 要求再次返回 A$`, returnTokenTurn)
}

func rememberTokenTurn(ctx context.Context) error {
	world, err := worldFrom(ctx)
	if err != nil {
		return err
	}
	return world.runTurn(ctx, "Remember this exact random token for later in this conversation: "+world.tokenA+". Reply with only ACK. Do not use tools.")
}

func returnTokenTurn(ctx context.Context) error {
	world, err := worldFrom(ctx)
	if err != nil {
		return err
	}
	prompt := "Reply with only the exact random token I asked you to remember in the previous turn. Do not use tools."
	if len(world.turns) >= 2 {
		prompt = "Again reply with only the exact random token from the first turn. Do not use tools."
	}
	return world.runTurn(ctx, prompt)
}

func registerSmokeAssertionSteps(sc *godog.ScenarioContext) {
	sc.Step(`^三个回答都应符合各轮 prompt$`, assertThreeTurnAnswers)
	sc.Step(`^turn2 和 turn3 都应包含准确的 A$`, assertThreeTurnAnswers)
	sc.Step(`^总共只应产生一个 ProcessInfo\(ProcessSpawn\)$`, assertExactlyOneSpawn)
	sc.Step(`^三轮只应产生一个 (Claude|CodeBuddy) PID$`, func(ctx context.Context, _ string) error {
		return assertExactlyOneSpawn(ctx)
	})
	sc.Step(`^三轮只应产生一个 app-server PID$`, assertExactlyOneSpawn)
	sc.Step(`^三轮 provider SessionID 应一致$`, assertStableResumeID)
	sc.Step(`^三轮应使用相同 threadId$`, assertStableResumeID)
	sc.Step(`^三个 RunID 应不同$`, assertDistinctRunIDs)
	sc.Step(`^provider ResumeID 应保持一致$`, assertStableResumeID)
	sc.Step(`^进程 PID 应保持一致$`, assertPersistentPIDAlive)
	sc.Step(`^普通对话应通过 control NDJSON 发送$`, assertCodeBuddyControlTransport)
}

func assertThreeTurnAnswers(ctx context.Context) error {
	world, err := worldFrom(ctx)
	if err != nil {
		return err
	}
	if len(world.turns) != 3 {
		return fmt.Errorf("got %d turns, want 3", len(world.turns))
	}
	if world.turns[0].result == nil || strings.TrimSpace(world.turns[0].result.Text) == "" {
		return errors.New("turn1 assistant text is empty")
	}
	for index := 1; index < 3; index++ {
		if world.turns[index].result == nil || !strings.Contains(world.turns[index].result.Text, world.tokenA) {
			return fmt.Errorf("turn%d did not return token %q", index+1, world.tokenA)
		}
	}
	return nil
}

func assertExactlyOneSpawn(ctx context.Context) error {
	world, err := worldFrom(ctx)
	if err != nil {
		return err
	}
	spawnCount := 0
	unique := map[int]struct{}{}
	for _, turn := range world.turns {
		for _, event := range turn.events {
			if process, ok := event.(adaptor.ProcessInfo); ok && process.Kind == adaptor.ProcessSpawn {
				spawnCount++
			}
		}
		for _, pid := range turn.spawnPIDs {
			unique[pid] = struct{}{}
		}
	}
	if spawnCount != 1 || len(unique) != 1 {
		return fmt.Errorf("spawn events=%d pids=%v, want one", spawnCount, sortedPIDs(unique))
	}
	for pid := range unique {
		if !processAlive(pid) {
			return fmt.Errorf("persistent CLI pid %d exited before Agent.Close", pid)
		}
	}
	return nil
}

func assertDistinctRunIDs(ctx context.Context) error {
	world, err := worldFrom(ctx)
	if err != nil {
		return err
	}
	if len(world.turns) != 3 {
		return fmt.Errorf("RunID assertion requires 3 turns, got %d", len(world.turns))
	}
	seen := make(map[string]struct{}, len(world.turns))
	for index, turn := range world.turns {
		if turn.result == nil || turn.result.RunID == "" {
			return fmt.Errorf("turn%d has empty RunID", index+1)
		}
		seen[turn.result.RunID] = struct{}{}
	}
	if len(seen) != 3 {
		return fmt.Errorf("got %d distinct RunIDs, want 3", len(seen))
	}
	return nil
}

func assertStableResumeID(ctx context.Context) error {
	world, err := worldFrom(ctx)
	if err != nil {
		return err
	}
	if len(world.turns) != 3 || world.turns[0].resumeID == "" {
		return errors.New("three valid checkpoints are required")
	}
	want := world.turns[0].resumeID
	for index, turn := range world.turns[1:] {
		if turn.resumeID != want {
			return fmt.Errorf("turn%d resume ID=%q, want %q", index+2, turn.resumeID, want)
		}
	}
	return nil
}

func assertPersistentPIDAlive(ctx context.Context) error {
	world, err := worldFrom(ctx)
	if err != nil {
		return err
	}
	if err := assertExactlyOneSpawn(ctx); err != nil {
		return err
	}
	for pid := range world.allPIDs {
		if !processAlive(pid) {
			return fmt.Errorf("persistent CLI pid %d exited before Agent.Close", pid)
		}
	}
	return nil
}

func assertCodeBuddyControlTransport(ctx context.Context) error {
	world, err := worldFrom(ctx)
	if err != nil {
		return err
	}
	if world.provider != providerCodeBuddy {
		return fmt.Errorf("control transport assertion used for %s", world.provider)
	}
	wantInput, wantOutput := false, false
	for _, turn := range world.turns {
		for _, event := range turn.events {
			notice, ok := event.(adaptor.Notice)
			if !ok || notice.Kind != adaptor.NoticeInvocation {
				continue
			}
			for _, arg := range eventArgs(notice.Data) {
				switch arg {
				case "--input-format=stream-json":
					wantInput = true
				case "--output-format=stream-json":
					wantOutput = true
				}
				if strings.Contains(arg, world.tokenA) {
					return errors.New("persistent prompt leaked into argv")
				}
			}
		}
	}
	if !wantInput || !wantOutput {
		return fmt.Errorf("control transport not observed: input=%t output=%t", wantInput, wantOutput)
	}
	return assertExactlyOneSpawn(ctx)
}

func eventArgs(data map[string]any) []string {
	switch args := data["args"].(type) {
	case []string:
		return append([]string(nil), args...)
	case []any:
		out := make([]string, 0, len(args))
		for _, value := range args {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func providerFromDisplayName(name string) (providerName, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude code":
		return providerClaude, nil
	case "codex":
		return providerCodex, nil
	case "codebuddy":
		return providerCodeBuddy, nil
	default:
		return "", fmt.Errorf("unknown provider display name %q", name)
	}
}

func sortedPIDs(values map[int]struct{}) []int {
	out := make([]int, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func filepathClean(path string) string {
	return strings.TrimSuffix(strings.TrimSpace(path), "/")
}
