# agent-adaptor

[English](./README.md) | [简体中文](./README.zh-CN.md) | [日本語](./README.ja.md) | [Deutsch](./README.de.md)

`agent-adaptor`는 단순하고 직관적인 API 한 세트를 제공하는 SDK로, `Codex`, `Claude Code`, `Cursor`, `CodeBuddy` 등 서로 다른 형태의 Agent를 통일된 방식으로 구동하고, 기본 호출을 넘어서는 여러 강화 기능을 제공한다.

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.6-sol"}))
result, err := agent.Run(ctx, "실패한 테스트를 수정해줘")
```

Claude Code로 바꾸려면 생성 코드의 Driver만 교체하면 되고, 나머지 코드는 그대로 둔다.

## 능력 개요

- **통일된 설정**: 하나의 API로 서로 다른 Agent의 skills/MCP/시스템 프롬프트/모델/sandbox/도구/승인을 제어한다.
- **스트리밍 응답**: 선택적인 스트리밍 출력으로, 상황에 맞게 사고 과정, 텍스트 출력, 도구 호출, 결정 요청을 식별한다.
- **세션 관리**: 대화의 매끄러운 이어가기와 분기를 지원한다. 업무 ID(티켓 번호, 사용자 ID 등)를 그대로 세션 식별자로 사용하며, 하위 계층의 복잡한 세션 관리 세부 사항을 신경 쓸 필요가 없다.
- **사람의 결정**: 콜백이나 이벤트로 질문에 답하고, 고위험 명령을 차단하고, 계획을 확인하기가 쉽다. 결정 회신 메커니즘이 내장되어 있어, 로컬에 한정하지 않고 결정을 클라우드에 영속화할 수 있다.

## 고급 기능

- **구조화 출력**: Go 구조체를 정의하고 `RunAs[T]`를 호출하기만 하면, Agent를 실행하면서 데이터가 채워진 객체를 반환하도록 제약할 수 있다.
- **다중 프로토콜 데코레이션**: A2A/AG-UI 등의 프로토콜 데코레이션이 내장되어 있어, 코드 한 줄로 Agent를 SSE + AG-UI 스트리밍 출력을 지원하는 표준 Agent로 감쌀 수 있다. 업무용 커스텀 프런트엔드나 클라이언트만 붙이면 완성된 Agent 서비스를 제공할 수 있다(실행 가능한 CopilotKit 프런트엔드 예제 포함).
- **Multi Agent**: Driver를 넘나드는 Team Agent 모드를 지원한다. 예를 들어 Codex를 Leader Agent로 두고 Plan Agent(Codex), Coding Agent(Claude), Reviewer Agent(Cursor)를 자율적으로 제어해 협업으로 작업을 완료하며, 모든 진행 상황과 출력은 자동으로 Leader Agent의 이벤트 스트림에 집계된다(examples/showcases/team-agent-workflow 예제 참고).
- **Agent 격리**: 선택한 설정을 별도 profile에 복제하여 여러 Agent를 병렬로 사용할 수 있다. 기존 AuthLink는 선언된 로그인 파일을 링크로 공유하며 인증 정보를 복사하지 않는다. provider의 링크된 인증 파일 갱신은 계속 공유된다.

## 설치

```bash
go get github.com/agent-dance/agent-adaptor
```

Go 1.26.8 이상이 필요하다.

중요: **실행 시점에 해당 Agent가 이미 설치되어 있고 로그인이 완료되어 있어야 한다**

## 빠른 시작

```go
package main

import (
	"context"
	"fmt"
	"log"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
)

func main() {
	agent := adaptor.New(
		codex.Driver(codex.Config{Model: "gpt-5.4"}),
		adaptor.WithWorkspace("/path/to/repository"),
	)

	result, err := agent.Run(context.Background(), "실패한 테스트를 수정해줘")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}
```

네 개의 내장 Driver는 생성 방식이 동일하며, 각자 자신의 `Config`를 가진다:

```go
codexAgent := adaptor.New(codex.Driver(codex.Config{}))
claudeAgent := adaptor.New(claude.Driver(claude.Config{}))
cursorAgent := adaptor.New(cursor.Driver(cursor.Config{}))
codeBuddyAgent := adaptor.New(codebuddy.Driver(codebuddy.Config{}))
```

## 스트리밍 실행

`Stream`은 한 번의 실행을 강타입 이벤트 스트림 하나로 펼쳐 내고, 끝날 때 `Result`를 준다:

```go
stream := agent.Stream(ctx, "커밋하려는 패치를 설명해줘")
defer stream.Cancel()

for event := range stream.Events() {
	switch event := event.(type) {
	case adaptor.TextDelta:
		fmt.Print(event.Text)
	case adaptor.Thinking:
		fmt.Fprint(os.Stderr, event.Text)
	case adaptor.ToolCall:
		if event.Phase == adaptor.PhaseStart {
			fmt.Printf("\n[도구 호출: %s]\n", event.Name)
		}
	case *adaptor.ApprovalRequest:
		_ = event.Approve(ctx)
	case adaptor.Dropped:
		log.Printf("백프레셔가 증분 이벤트 %d개를 버렸다", event.Count)
	}
}

result, err := stream.Result()
```

텍스트, 사고, 도구 호출과 결과, 프로세스 정보, 생명주기, 하위 Agent 진행 상황, 승인 요청이 모두 이 하나의 스트림에 있고, 두 번째 채널은 없다.

소비를 미리 끝낼 때는 `Cancel()`을 호출하며, 이는 멱등하다.

## 사람의 승인과 sandbox

sandbox 강도, 네트워크 접속과 브라우저 도구, 승인 모드는 같은 `Policy` 안에 있다. 생성 지점은 기본값이고, `Run` / `Stream` 지점에서 호출마다 전체를 덮어쓸 수 있다:

```go
reviewer := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Sandbox:   adaptor.ReadOnly,    // 읽기 전용 workspace, 리뷰나 계획 성격의 역할에 적합
		WebSearch: adaptor.FeatureDeny, // 웹 검색을 명시적으로 끈다
		Browser:   adaptor.FeatureDeny,
		Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAsk, // 고위험 명령은 사람에게 넘긴다
			PlanReview: adaptor.ApprovalAsk,
			Question:   adaptor.QuestionAsk, // 기본값은 질문 자동 거부
			Timeout:    2 * time.Minute,
			OnTimeout:  adaptor.FallbackAbort,
		},
	}),
)
```

sandbox는 `ReadOnly`, `WorkspaceWrite`, `Unrestricted` 세 단계가 있고, `PolicyReadOnly` 같은 프리셋은 `Sandbox`만 설정한 단축값이다. 선택한 Driver가 어떤 차원을 지원하지 않으면, 조용히 낮추는 대신 프로세스를 시작하기 전에 명확하게 오류를 낸다.

승인에는 두 가지 소비 형태가 있고, 둘 중 하나를 고른다. 콜백을 걸면 콜백 방식이며, CLI와 무인 운용에 적합하다:

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		switch req.Kind {
		case adaptor.ApprovalPermission:
			return req.Approve(ctx)
		case adaptor.ApprovalQuestion:
			return req.Answer(ctx, "PostgreSQL을 사용한다")
		default:
			return req.Deny(ctx, "계획은 사람의 확인이 필요하다")
		}
	}),
)
```

무인 운용이라면 이미 준비된 `adaptor.ApproveAll()`과 `adaptor.DenyAll(reason)`을 그대로 써도 된다.

콜백을 걸지 않으면 이벤트 방식이다. 요청이 `*adaptor.ApprovalRequest`로 이벤트 스트림에 나타나고 responder를 함께 가지고 있어서, 먼저 보류해 두었다가 이후 임의의 goroutine이나 다른 HTTP 요청에서 회신할 수 있다. 이것이 바로 Web 시나리오에 필요한 형태다:

```go
for event := range stream.Events() {
	switch event := event.(type) {
	case *adaptor.ApprovalRequest:
		pending.Add(threadKey, event) // 요청을 보류하고 프런트엔드에 밀어 렌더링한다
	case adaptor.Notice:
		// SDK는 확정된 모든 결정을 브로드캐스트하며, 정책 자동 승인과 타임아웃 폴백도 포함하므로
		// 미결 목록을 호스트가 직접 대조할 필요가 없다.
		if event.Kind == adaptor.NoticeApprovalResolved {
			if id, ok := event.Data["request_id"].(string); ok {
				pending.Remove(threadKey, id)
			}
		}
	}
}
```

`pending`은 호스트 자신의 저장소이며, 프런트엔드가 요청을 받은 뒤 별도의 HTTP 요청에서 결정을 회신한다:

```go
func (h *host) resolveDecision(w http.ResponseWriter, r *http.Request) {
	req := h.pending.Take(threadKey, requestID)
	if err := req.Approve(r.Context()); err != nil {
		sse.WriteApprovalError(w, err) // 이미 확정/이미 만료 → 410, Kind 불일치 → 400
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

응답은 exactly-once다. 중복 응답, Kind 불일치, 이미 끝난 실행은 모두 안정적인 오류를 반환하고(`ErrApprovalResolved`, `ErrApprovalKindMismatch`, `ErrApprovalExpired`), 제로값 요청도 영구히 블로킹되지 않는다. 아무도 응답하지 않으면 `Policy.Approvals`의 `OnTimeout`으로 폴백하고, 거부된 뒤에는 `OnReject`를 따른다. 보류한 요청을 어디에 저장할지는 호스트가 결정하며, 프로세스 메모리에 한정되지 않는다.

완전히 실행 가능한 Web HITL 경로는 [`web-chat/copilotkit`](./examples/web-chat/copilotkit)에 있다. `/decision/pending`과 `/decision/resolve` 두 엔드포인트가 있고, 페이지를 새로 고쳐도 미결 결정이 복원된다.

## 멀티턴 세션

Agent는 기본적으로 무상태다. 대화 연속성이 필요하면 store 하나를 주입하면 된다:

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithThreadStore(memory.NewStore()),
)
defer agent.Close(context.Background())

thread := agent.Thread("tenant-42/issue-123")        // 매핑된 세션이 이미 있으면 이어가고, 없으면 생성한다
result, err := thread.Run(ctx, "이 문제를 계속 조사해줘")

only := agent.Thread("tenant-42/issue-123", adaptor.ResumeOnly()) // 이어가기만 하고 생성하지 않는다
branch := thread.Fork("tenant-42/issue-123/plan-b")               // 현재 진행 지점에서 분기한다
```

몇 가지 약속:

- **세션 key는 업무 측 자신의 문자열이다.** SDK는 그대로 저장하고 그대로 비교한다. 완전히 새로운 대화를 시작할 때는 key를 바꾸며, SDK는 기존 key를 새 세션에 다시 바인딩하는 입구를 제공하지 않는다.
- **같은 Thread에는 동시에 한 번의 실행만 있다.** lease로 보장하며, 만료된 실행이 새 상태를 덮어쓰지 않는다.
- **이어가기 전에 호환성을 검증한다.** Driver, 모델, 해석된 실제 workspace, 설정, skills, MCP가 모두 fingerprint 계산에 참여하고, 그중 어느 하나라도 어긋나면 세션을 잘못 재사용하지 않는다.
- **실패는 상태를 오염시키지 않는다.** 0이 아닌 종료, 프로토콜 오류, 취소는 유효한 checkpoint를 만들지 않으며, 이전의 건강한 세션 기록은 그대로 유지된다.
- **상주 프로세스는 기본적으로 재사용한다.** Windows, macOS, Linux에서 Claude, CodeBuddy, Codex는 명시적인 Thread의 턴 사이에 같은 프로세스를 재사용하며, 특정 턴이나 매 턴에 새 프로세스가 필요하면 `adaptor.WithSpawn()`을 추가한다. Cursor와 무상태 호출은 항상 매 턴 새 프로세스를 시작한다. `Close` 이후의 실행은 `ErrAgentClosed`를 반환한다.

단일 프로세스 시나리오에서는 `memory.NewStore()`를 쓰고, 영속화가 필요하면 `threadstore.Store`를 구현한다.

## 구조화 출력

```go
type ReleasePlan struct {
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Summary   string `json:"summary"`
	Content   string `json:"content"`
}

plan, result, err := adaptor.RunAs[ReleasePlan](ctx, agent,
	"Produce the release plan as a Markdown file artifact.")
if err != nil {
	return err
}
fmt.Printf("%s (%s)\n%s\n", plan.Filename, result.RunID, plan.Content)
```

Schema는 Go 타입에서 생성되며, 각 provider의 네이티브 schema 제약을 우선 사용한다. 현재 채널이나 정책이 지원하지 않으면 자동으로 프롬프트 제약과 로컬 검증으로 폴백하고, 둘 다 불가능할 때만 실행 전에 실패한다. 반환값에는 typed 값과 완전한 감사용 `Result`가 모두 담긴다.

자세한 내용은 [`structured-output` 예제](./examples/structured-output)와 [구조화 출력 문서](./docs/structured-output.md)를 참고한다.

## 옵션과 리소스

옵션은 한 세트의 어휘만 있고, 작용 범위는 타입으로 컴파일 시점에 구분한다:

| 타입 | 쓸 수 있는 곳 |
|---|---|
| `Option` | `adaptor.New`에만 사용 |
| `CallOption` | `Run` / `Stream`에만 사용 |
| `SharedOption` | 두 곳 모두 사용 가능, 호출 지점이 생성 지점을 덮어쓴다 |

병합 규칙은 하나뿐이다. 가까운 쪽이 먼 쪽을 덮어쓰고, skills는 추가되며, 나머지는 각자의 약속에 따라 교체되거나 병합된다.

같은 옵션 세트가 각 Agent의 주요 설정 면을 커버한다:

| 제어하려는 것 | 사용할 것 |
|---|---|
| 모델 | `WithModel` |
| 추가 지침 | `WithInstructions` |
| 네이티브 추가 | `WithAppendSystemPrompt` |
| 작업 디렉터리 | `WithWorkspace`, 격리된 워크트리는 `WithWorkspaceSpec` |
| skills | `WithSkills`에 `skill.Dir` / `skill.FS` / `skill.Inline` / `skill.Key` / `skill.Require` 조합 |
| MCP | `WithMCP`에 `mcp.Stdio` / `mcp.HTTP` / `mcp.SSE` 조합 |
| sandbox, 네트워크, 브라우저 도구, 승인 | `WithPolicy`, 대화형이면 `OnApproval` 추가 |
| 설정 디렉터리와 리소스 | `WithProfile`, `WithProfileResources` |
| 타임아웃, 감사 메타데이터, 호출자 identity | `WithTimeout`, `WithMetadata`, `WithIdentity` |
| 세션 영속화 | `WithThreadStore` |

```go
agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithModel("gpt-5.4"),
	adaptor.WithInstructions("당신은 이 저장소의 리뷰어입니다: 코드만 읽고, 결론을 먼저 제시한 뒤 근거를 제시하세요."),
	adaptor.WithSkills(skill.Dir("./skills/review")),
	adaptor.WithMCP(mcp.Stdio("repo-tools", "repo-mcp", mcp.Args("serve"))),
	adaptor.WithProfile(profile.Dedicated("./profiles/reviewer")),
	adaptor.WithTimeout(10*time.Minute),
)

result, err := agent.Run(ctx, "이 변경을 리뷰해줘",
	adaptor.WithModel("gpt-5.4-mini"),
	adaptor.WithSkills(skill.Require(skill.Dir("./skills/security"), "이번에는 반드시 보안 검사를 통과해야 한다")), // 추가되며 기본 skills를 밀어내지 않는다
	adaptor.WithMetadata("request_id", requestID),
)
```

같은 설정에서 Driver만 바꾸면 다른 Agent가 된다. 어떤 Driver가 그중 특정 능력을 지원하지 않으면, 조용히 무시하지 않고 시작 전에 명확하게 오류를 낸다.

```go
codexReviewer := adaptor.New(codex.Driver(codex.Config{}), reviewerOptions...)
claudeReviewer := adaptor.New(claude.Driver(claude.Config{}), reviewerOptions...)
```

`WithAppendSystemPrompt`는 설정된 Driver의 SystemPrompt.Append 선언이 필요한
별도 네이티브 추가 채널이다. 가까운 범위의 원본 바이트로 교체하고 빈 문자열로
해제한다. provider 기본값, `WithInstructions`, 사용자 Prompt는 대체하지 않는다.

Claude는 소유한 0600 파일, CodeBuddy는 네이티브 argv, Codex는 exec TOML 또는
app-server RPC의 developer instructions를 사용한다. Cursor는 빈 값이 아닌 추가를 거부한다.
CodeBuddy와 Codex exec는 32768 UTF-8 바이트까지 허용하며 최종 명령도 검사한다.
[제한](./docs/api-reference.md#31-dual-scope-options)과 [관측 표](./docs/streaming.md#provider-observation-support)를 참고한다.
Codex의 typed skill 입력 수락은 실행 증명이 아니며 Cursor는 MCP와 사용자 정의 하위 agent를 관측한다.
Delegation은 손실 가능한 UI bus보다 먼저 같은 observer에 사실을 전달하고 호출마다
[독립된 예산](./docs/run-policy.md#delegation-active-execution-budget)을 갖는다.

`Policy.ActiveExecutionTimeout`은 해당 실행의 Ask 대기를 제외한 실행 시간을
제한한다. 0은 무제한, 음수는 오류이며 WithPolicy는 전체 값을 교체한다.
WithTimeout과 부모·승인 deadline은 계속 벽시계 기준이다. 예산은 원자적 저장 전에
확정되며 Finalize는 원래 취소 가능한 context에서 성공해야 한다.
[자세한 계약](./docs/run-policy.md#active-execution-budget)을 참고한다.

명시적 Store를 받는 선택적 [`capabilityrecorder.Recorder.Option()`](./docs/api-reference.md#121-capability-recording)으로
scope/RunID별 실행 중 이력을 조회할 수 있다. A2A capability/Todo 공개는 각각
opt-in이다. 관측이 없다고 미실행 또는 완전한 감사 기록을 증명하지는 않는다.

루트 API의 `With*` 함수는 `WithEventMeta`를 포함해 27개이며 네이티브 추가에는
`WithAppendSystemPrompt`만 사용한다. Todo는 ToolCall 및 Transcript와 함께 제공되며
PlanReview 승인과 다르다. 능력 기록은 최선의 관측 결과이며 권한 부여 결정이나 완전한
감사·과금 증거가 아니다. 리소스 생성, 네이티브 입력 수락, 공식 프로토콜 fixture와 실제
provider 실행은 각각 별도의 증거다.

`go run ./examples/offline`으로 [오프라인 예제](./examples/offline)를 실행할 수 있다.
스크립트형 fake Driver로 추가 값 덮어쓰기, 승인 이벤트, Todo 비우기, scope별 조회와
예산 초과 후 부분 결과를 확인한다. CLI·인증 정보·provider 호출이 필요하지 않으며
출력은 실제 provider 검증을 뜻하지 않는다.

## 호스트 정의 Tools

typed Go 함수로 Agent에 능력을 바로 추가하며, MCP server를 직접 만들고 유지할 필요가 없다:

```go
type SearchInput struct {
	Query string `json:"query" jsonschema:"required"`
}

type SearchOutput struct {
	Files []string `json:"files"`
}

searchRepo := tool.Define(
	"search_repo",
	"Search files in the current repository.",
	func(ctx context.Context, in SearchInput) (SearchOutput, error) {
		return search(ctx, in.Query)
	},
	tool.ReadOnly(),
	tool.Idempotent(),
	tool.Revision("search_repo/v1"),
)

agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithTools(searchRepo),
)
defer agent.Close(context.Background())
```

`WithTools`는 생성 시점에만 쓸 수 있고, Tool 집합을 통째로 교체한다. schema는 기본적으로 handler의 Go 타입에서 추론하며, 표준 JSON Schema를 명시적으로 줄 수도 있다. `tool.Reject(code, message)`는 모델에 안전하게 보여 줄 수 있는 업무 실패를 뜻하고, 일반 error와 panic은 정제된다. 상태가 있는 Thread에서 쓰는 모든 Tool에는 `tool.Revision`을 설정해, handler의 동작 변화가 이어가기 호환성 판정에 들어가게 해야 한다.

여기서 MCP는 내부 전달 메커니즘일 뿐이다. 이미 있는 MCP server나 원격 MCP server는 여전히 `WithMCP`를 쓰고, 내장 Driver는 Tools를 SDK 자체의 격리된 profile에 물화하며, 설정해 둔 네이티브 profile은 건드리지 않는다. 생명주기, schema, 오류, 보안과 Thread 의미는 [호스트 정의 Tools 계약](./docs/tools.md)을 참고한다.

## Agent 격리

`WithProfile`은 이 Agent가 어떤 provider 설정 디렉터리를 쓸지 결정한다. `profile.CloneNative`는 로컬 머신의 네이티브 설정에서 독립적인 profile을 하나 복제하며, settings, MCP, skills를 함께 가져올지 선택할 수 있다. 로그인 상태는 token을 복사하는 대신 공유 링크로 처리한다:

```go
worker := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithProfile(profile.CloneNative("/var/agents/worker-1",
		profile.CopySettings(),
		profile.CopyMCP(),
		profile.CopySkills(),
		profile.LinkAuth(), // 심볼릭 링크 방식으로 로컬 로그인 상태를 공유하며, 로컬 로그인 상태 변경이 자동으로 따라온다
	)),
)
```

그래서 같은 CLI로 역할별 또는 작업별로 여러 인스턴스를 열어 병렬로 돌릴 수 있고, 각자의 설정 변경이 서로 영향을 주지 않으며, 로컬에서 사용 중인 `~/.claude`, `~/.codex`도 건드리지 않는다:

```go
isolated := func(dir string) adaptor.Option {
	return adaptor.WithProfile(profile.CloneNative(dir,
		profile.CopySettings(), profile.LinkAuth()))
}

planner := adaptor.New(codex.Driver(codex.Config{}),
	isolated("/var/agents/planner"),
	adaptor.WithPolicy(adaptor.PolicyReadOnly),
)
implementer := adaptor.New(claude.Driver(claude.Config{}),
	isolated("/var/agents/implementer"),
	adaptor.WithWorkspace("/repo/worktrees/feature-x"),
)
```

다른 세 가지 선택지도 있다. `profile.Native()`는 로컬 네이티브 설정을 그대로 쓴다. `profile.Dedicated(dir)`는 직접 관리하는 디렉터리에 고정한다. `profile.CloneFrom(src, dst, ...)`는 템플릿 디렉터리에서 파생한다. profile은 세션 fingerprint에 참여하므로 생성 시점 옵션만 될 수 있고, 호출마다 바꿀 수 없다.

비어 있지 않은 `WithTools`와 명시적 `profile.Dedicated(source)`를 함께 사용하면
소유한 실행 clone은 `Agent.Close` 후에도 유지되고 기존 source와 분리된다.
최종 이력 정리는 호스트가 담당한다. Native/clone 선택은 임시 hosted profile을 쓴다.
삭제된 세션 파일은 resume ID로 복구할 수 없다.
[소유권·충돌·복구](./docs/tools.md#persistent-dedicated-profiles)를 참고한다.

`agent.ProfileState(ctx)`는 리소스의 desired/observed 상태를 읽고 `agent.SyncProfile(ctx)`는 리소스를 생성한다. 어느 작업도 provider가 리소스를 호출했다는 증거는 아니다. 전체 흐름은 [`profiles` 예제](./examples/profiles)를 참고한다.

## 결과와 오류

성공하면 `*Result, nil`을 반환한다. `Driver.Run` 진입 후 모든 실패는 `nil, *RunError`를 반환하며, nil이 아닌 `Result`에 관찰한 결과를, `Cause`에 원래 오류 체인을 보존한다. `Reason`은 주원인을 나타낸다. 실행 전 실패는 일반 래핑 오류이며, 실패 판정은 Go의 `error` 경로 하나만 사용한다.

```go
result, err := agent.Run(ctx, prompt)
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		log.Printf("실행 실패: %s; 확보된 요약: %s", runErr.Reason, runErr.Result.Summary)
	}
	return err
}
```

`Result`의 각 계층 출력은 서로를 오염시키지 않는다:

| 필드 | 내용 |
|---|---|
| `Text` | 최종적으로 사용자를 향하는 응답 텍스트 |
| `Summary` | 목록, 로그, issue 댓글에 적합한 짧은 요약 |
| `Raw()` | 완전한 stdout, stderr, 그리고 각 provider 정식 프로토콜의 종국 payload |
| `Transcript()` | Driver가 정식 프로토콜에서 파싱한 표준화 항목 |
| `Services()` | 이번 실행에서 실제로 관찰된 runtime services |
| `Decode()` | 검증이 끝난 구조화 출력 |
| `Usage` / `Model` / `Provider` / `Metadata` | 사용량과 감사 정보 |

`Text`에는 원시 stdout이 섞이지 않고, 요약이나 각 provider의 종국 payload가 자동으로 붙지도 않는다. `Run`과 `Stream.Result()`로 얻는 내용은 필드 단위로 동등하다.

## 상위 애플리케이션 연동

**Web 프런트엔드**, 한 줄로 Agent를 `http.Handler`로 감싸 AG-UI 프로토콜을 태우면, AG-UI 호환 클라이언트(예: CopilotKit)가 바로 연결할 수 있다:

```go
mux.Handle("/agent", sse.Handler(agent, sse.Options{
	Protocol: sse.AGUI,
}))
```

**A2A**, `bridges/a2a`는 임의의 Runner를 A2A server로 발행하고, 호스트는 라우팅, 인증, TLS만 담당한다:

```go
server := bridgea2a.NewServer(agent, bridgea2a.ServerOptions{
	AgentCard: bridgea2a.AgentCard{
		Name:        "Local coding agent",
		Description: "Runs coding tasks through agent-adaptor",
		Version:     "1.0.0",
		URL:         "https://host.example/a2a",
	},
	Session: bridgea2a.ThreadByContextID(), // 원격 contextID를 로컬 Thread key로 안정적으로 매핑한다
	Options: []adaptor.CallOption{adaptor.WithPolicy(adaptor.PolicyWorkspaceWrite)},
})

mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
mux.Handle("/a2a", server.Handler())
```

원격 A2A Agent를 역방향으로 호출할 때는 `clients/a2a`를 쓴다. 이것이 반환하는 것은 A2A의 task, message, artifact이며, 원격 프로토콜 작업에 로컬 CLI의 stdout이나 `Result`가 있는 것처럼 위장하지 않는다:

```go
client := clienta2a.New(clienta2a.Options{
	AgentCardURL: "https://remote.example/.well-known/agent-card.json",
	Auth:         clienta2a.BearerTokenFromEnv("REMOTE_A2A_TOKEN"),
})
defer client.Close()

task, err := client.Send(ctx, clienta2a.SendRequest{
	Message: clienta2a.Message{
		Role:  "user",
		Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "이 변경을 리뷰해줘"}},
	},
})
```

중간 과정이 필요하면 `SendStream` / `Subscribe`를 쓴다. 사고 과정, 도구 호출, 승인 이벤트, 진단 필드를 외부에 노출할지는 `ExposurePolicy`가 제어하며, 기본값은 최소 노출이다.

## 멀티 Agent 협업

`agent-adaptor`는 A2A 표준 프로토콜로 Driver를 넘나드는 멀티 Agent 협업을 지원한다(따라서 임의의 원격 A2A 프로토콜 Agent도 지원한다).

Driver를 넘나드는 협업의 가치는 모델과 그 네이티브 `Harness` 사이의 적합성 우위를 지키는 데 있다. GPT 계열 모델은 Codex에서 더 잘 동작하고, Claude 계열 모델은 Claude Code에서 능력이 더 강하다. 그래서 `agent-adaptor`의 설계 지향은 각 모델을 가장 잘 맞는 Harness에 남겨 둔 채 협업에 참여시키는 것이며, 멀티 모델 협업을 열기 위해 여러 모델을 지원하지만 성능은 좋지 않은 범용 Harness에 맞추는 것이 아니다.

핵심 코드 예시는 다음과 같다:

```go
team, err := a2adelegation.NewService(a2adelegation.Config{
	Agents: []a2adelegation.AgentRef{
		a2adelegation.LocalNamed("plan", "Codex Planner", planner, a2adelegation.Policy{}),
		a2adelegation.LocalNamed("impl", "Claude Code Implementer", implementer, a2adelegation.Policy{}),
		a2adelegation.LocalNamed("review", "Codex Reviewer", reviewer, a2adelegation.Policy{}),
	},
})
if err != nil {
	return err
}
defer team.Close()

leader := adaptor.New(leaderDriver, team.Option())
stream := leader.Stream(ctx, "Plan, implement, and review TASK.md")
for event := range stream.Events() {
	if update, ok := event.(adaptor.SubagentUpdate); ok {
		fmt.Printf("[%s] %s: %s\n", update.Agent, update.Kind, update.Delta)
	}
}
```

완전한 [`team-agent-workflow`](./examples/showcases/team-agent-workflow)에는 역할 단위 sandbox, 구조화된 `PLAN.md` 산출물, workspace 감사, 그리고 실시간 하위 Agent 카드가 있는 CopilotKit 페이지가 들어 있고, 명령 하나로 시작한다:

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
```

## 환경 프로브

`Agent.Inspect()`는 읽기 전용 프로브로, 시작 전 점검, 환경 진단, 모델 선택에 쓴다. 지원하지 않는 프로브는 명확하게 unsupported를 반환하며, 데이터를 만들어 내지 않는다:

```go
environment, err := agent.Inspect().Environment(ctx) // 건강 상태와 항목별 진단, 바로 렌더링 가능
models, err := agent.Inspect().Models(ctx)
quota, err := agent.Inspect().Quota(ctx)
state, err := agent.ProfileState(ctx)                // 기대값과 실제값만 보고하고 변경하지 않는다
synced, err := agent.SyncProfile(ctx)                // 설정 리소스를 명시적으로 물화한다
```

## 여섯 개의 명사

라이브러리 전체의 공개 모델은 여섯 개의 명사뿐이다:

| 명사 | 의미 |
|---|---|
| `Agent` | 설정이 완전해 생성하면 바로 실행할 수 있는 에이전트 |
| `Thread` | 업무 key로 식별되며 이어가기와 분기가 가능한 한 단락의 대화 |
| `Stream` | 진행 중인 한 번의 실행 |
| `Event` | 실행 과정에서 발생한 하나의 강타입 이벤트 |
| `Result` | 한 번의 실행의 최종 결과와 감사 정보 |
| `Driver` | 어떤 Agent CLI의 접속 구현으로, 확장하는 쪽만 신경 쓰면 된다 |

이에 따르는 제약은 하나의 생성 입구, 한 세트의 옵션 병합 규칙, 하나의 실행 파이프라인, 하나의 이벤트 스트림, 하나의 실패 판정 입구다.

## 패키지 일람

| 패키지 | 용도 |
|---|---|
| [`driver`](./driver) | Driver SPI, 새 Agent를 접속할 때 사용 |
| [`codex`](./codex), [`claude`](./claude), [`cursor`](./cursor), [`codebuddy`](./codebuddy) | 내장 Driver와 각자의 Config |
| [`tool`](./tool), [`skill`](./skill), [`mcp`](./mcp), [`profile`](./profile) | 호출자를 향한 능력과 리소스 어휘 |
| [`threadstore`](./threadstore), [`memory`](./memory) | Thread 영속화 인터페이스와 메모리 구현 |
| [`bridges`](./bridges) | SSE, AG-UI, A2A, subagent-stream 프로토콜 브리지 |
| [`clients/a2a`](./clients/a2a) | A2A 클라이언트 |
| [`hosttools`](./hosttools) | 선택적인 위임 오케스트레이션과 이벤트 기록 컴포넌트 |
| [`adaptertest`](./adaptertest) | Driver 일관성 테스트 스위트 |

자체 Agent CLI를 접속하려면 `driver.Driver`를 구현하고 `adaptertest`를 통과시키면 되며, 그다음부터는 내장 Driver와 같은 상위 능력을 가진다.

## 예제

- [`quickstart`](./examples/quickstart): Agent를 생성해 prompt를 한 번 실행한다.
- [`streaming`](./examples/streaming): 이벤트 소비와 취소.
- [`threads`](./examples/threads): 이어가기, 이어가기만 하고 생성하지 않기, 분기와 checkpoint 감사.
- [`structured-output`](./examples/structured-output): typed JSON 출력.
- [`tools`](./examples/tools): 실제 로컬 provider에 typed Go 함수를 노출하며, MCP를 직접 관리하지 않는다.
- [`skills`](./examples/skills) / [`profiles`](./examples/profiles): skill 해석과 물화, 설정 리소스와 동기화.
- [`inspect`](./examples/inspect): 환경, 모델, 쿼터, schema, skills와 설정 상태.
- [`web-chat`](./examples/web-chat): SSE/AG-UI 서버, [`aguiclient`](./examples/web-chat/aguiclient)와 [`copilotkit`](./examples/web-chat/copilotkit) 두 프런트엔드 포함.
- [`a2a-server`](./examples/a2a-server): A2A로 Agent를 발행하고 호출한다.
- [`showcases/team-agent-workflow`](./examples/showcases/team-agent-workflow): 계획, 구현, 리뷰를 하나의 파이프라인으로 엮는다.

실제 호출이 필요한 예제는 해당 CLI와 로그인 상태에 의존한다. 저장소의 일반 테스트는 유료 호출을 발생시키지 않는다.

## 경계

핵심 라이브러리는 HTTP/gRPC server, 큐, 스케줄러, 멀티테넌시, 인증, 데이터베이스를 제공하지 않고, 어떤 작업을 어느 Agent에 넘길지도 호출자를 대신해 결정하지 않는다. 프로토콜 서비스는 bridges와 상위 애플리케이션에 남기고, 팀 역할과 프로세스 정책은 업무 측에 남긴다.

## 문서

- [문서 맵](./docs/README.md)
- [API reference](./docs/api-reference.md)
- [호스트 정의 Tools](./docs/tools.md)
- [스트리밍 가이드](./docs/streaming.md)
- [구조화 출력](./docs/structured-output.md)
- [실행 정책: sandbox, 승인, 타임아웃](./docs/run-policy.md)
- [A2A 연동](./docs/a2a.md)
- [공개 오류](./docs/public-errors.md)

## 라이선스

별도로 명시된 경우를 제외하면 이 저장소는
[Apache License, Version 2.0](./LICENSE)에 따라 배포됩니다. 제3자 자료에는
각 자료의 라이선스와 저작자 표시가 그대로 적용됩니다. 자세한 내용은
[제3자 고지](./THIRD_PARTY_NOTICES.md)를 참조하십시오. 정식 조건은 `LICENSE`의
영문 원문을 따릅니다.

Codex, Claude, Cursor, CodeBuddy 및 기타 제품명은 각 권리자의 상표입니다.
이 명칭은 지원되는 연동을 식별하기 위한 용도로만 사용되며, 이 프로젝트는 해당
권리자와 제휴 관계가 없고 권리자의 보증을 받지 않습니다.
