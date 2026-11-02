# agent-adaptor

[English](./README.md) | [简体中文](./README.zh-CN.md) | [日本語](./README.ja.md) | [Deutsch](./README.de.md)

`agent-adaptor` v2는 `Codex`, `Claude Code`, `Cursor`, `CodeBuddy`를 작고 직관적인 하나의 API로 실행하는 SDK입니다. 단순한 호출 외에도 대화 관리, 메시지 전달, 승인 등 다양한 기능을 제공합니다.

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
result, err := agent.Send(ctx, "Fix the failing tests").Result()
```

Claude Code로 바꾸려면 생성자의 Driver만 교체하면 됩니다. 나머지 코드는 그대로 사용할 수 있습니다.

## 주요 기능

- **통합 설정**: 하나의 API로 여러 에이전트의 skills, MCP, 시스템 프롬프트, 모델, 샌드박스, 도구, 승인을 설정합니다.
- **스트리밍 응답**: 필요에 따라 스트리밍 출력을 사용하고, 추론, 텍스트, 도구 호출, 의사 결정 요청을 구분합니다.
- **대화 관리**: 대화를 이어가거나 분기할 수 있습니다. 티켓 번호나 사용자 ID 같은 업무 식별자를 대화 key로 사용하며, 하위 세션 관리는 SDK가 처리합니다.
- **사람의 의사 결정**: 콜백이나 이벤트로 질문에 답하고, 위험한 명령을 가로채고, 계획을 승인합니다. 호스트가 응답 객체를 보관했다가 다른 HTTP 요청에서 응답할 수 있으며, 승인 감사 기록의 저장은 호스트가 담당합니다.

## 고급 기능

- **구조화 출력**: Go 구조체를 정의하고 `RunAs[T]`를 호출하면, 에이전트가 출력 제약에 따라 실행되어 값이 채워진 객체를 반환합니다.
- **여러 프로토콜 지원**: 내장 A2A/AGUI 어댑터를 한 줄로 적용해 Agent를 SSE + AGUI 스트리밍을 지원하는 표준 에이전트로 노출합니다. 사용자 정의 프런트엔드나 클라이언트를 연결해 에이전트 서비스를 구성할 수 있으며, 실행 가능한 CopilotKit 프런트엔드도 포함되어 있습니다.
- **팀**: 호스트가 선택한 leader가 각기 다른 Driver의 멤버나 Remote A2A 멤버를 조율합니다. 예를 들어 Codex leader에 계획 담당(Codex), 구현 담당(Claude), 리뷰 담당(Cursor)을 연결할 수 있습니다. 작업 업데이트와 worker 승인 요청은 leader의 Event 스트림에 모입니다. [팀 가이드](./docs/team.md)와 examples/showcases/team-agent-workflow 쇼케이스를 참고하세요.
- **Agent 격리**: 로컬 에이전트 설정을 전용 디렉터리에 복제하고 로그인 상태를 링크로 공유하여 설정 변경을 격리합니다. 병렬 개발이나 역할 분담을 위해 여러 Codex/Claude Code 인스턴스를 동시에 실행할 수 있습니다.

## 설치

```bash
go get github.com/agent-dance/agent-adaptor/v2
```

Go 1.26.5 이상이 필요합니다.

**실행할 에이전트의 CLI를 미리 설치하고 인증을 마쳐야 합니다.**

## 빠른 시작

```go
package main

import (
	"context"
	"fmt"
	"log"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/codex"
)

func main() {
	agent := adaptor.New(
		codex.Driver(codex.Config{Model: "gpt-5.4"}),
		adaptor.WithWorkspace("/path/to/repository"),
	)

	result, err := agent.Send(context.Background(), "Fix the failing tests").Result()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}
```

내장 Driver 네 개는 모두 같은 방식으로 생성하며, 각자 고유한 `Config` 타입을 사용합니다.

```go
codexAgent := adaptor.New(codex.Driver(codex.Config{}))
claudeAgent := adaptor.New(claude.Driver(claude.Config{}))
cursorAgent := adaptor.New(cursor.Driver(cursor.Config{}))
codeBuddyAgent := adaptor.New(codebuddy.Driver(codebuddy.Config{}))
```

## 스트리밍 실행

`Send`는 실행을 시작하고 `Stream`을 반환합니다. `Events()`에서 실시간 진행 상황을 읽거나, `Result()`로 동기적으로 완료를 기다립니다.

```go
stream := agent.Send(ctx, "Explain the patch that is about to be committed")
defer stream.Cancel()

for event := range stream.Events() {
	switch event := event.(type) {
	case adaptor.TextDelta:
		fmt.Print(event.Text)
	case adaptor.Thinking:
		fmt.Fprint(os.Stderr, event.Text)
	case adaptor.ToolCall:
		if event.Phase == adaptor.PhaseStart {
			fmt.Printf("\n[tool call: %s]\n", event.Name)
		}
	case *adaptor.ApprovalRequest:
		_ = event.Approve(ctx)
	case adaptor.Dropped:
		log.Printf("backpressure dropped %d incremental events", event.Count)
	}
}

result, err := stream.Result()
```

텍스트, 추론, 도구 호출과 결과, 프로세스 정보, 생명주기, 메시지 처리 결과, 승인 요청이 모두 이 스트림을 통해 전달됩니다. provider의 각 턴은 `TurnEnd`를 발생시킵니다. 마지막 이벤트는 검증, checkpoint 처리, 정리가 끝난 뒤 발행되는 `Done{Result, Err}`이며, `stream.Result()`와 같은 결과 포인터와 오류를 담습니다. `RunFinished`는 참고 정보입니다.

이벤트 소비를 일찍 중단할 때는 `Cancel()`을 호출하세요. 여러 번 호출해도 안전합니다.

## 실행 중 메시지 전달

반환된 Stream에 추가 지시를 보낼 수 있습니다. 호스트의 안정적인 ID를 지정해 중복을 제거합니다.

```go
stream := agent.Send(ctx, "Fix the failing tests")
err := stream.Send(ctx, "Keep the public API unchanged",
	adaptor.MessageID("review-comment-42"),
	adaptor.From(adaptor.Author{Kind: adaptor.AuthorHuman, ID: "reviewer"}),
	adaptor.When(adaptor.Soon),
)
```

기본값인 `Soon`은 지원되는 가장 빠른 경계에서 전달하며, 필요하면 턴이 끝난 뒤 이어서 실행합니다. `AfterTurn`은 후속 턴까지 기다립니다. `Interrupt`는 현재 턴을 중단한 뒤 같은 실행 안에서 계속하며, 지원하지 않는 Driver는 `ErrMessageUnsupported`로 거절합니다. [Driver 지원 표](./docs/streaming.md#12-message-delivery-capabilities)를 참고하세요.

전송 오류가 nil이면 메시지가 접수되었다는 뜻입니다. `MessageUpdate`가 접수, 전달, 실패 상태를 알리며, 실패에는 원문이 포함됩니다. `Result.Messages()`는 최종 처리 결과를 기록하고, `Undelivered()`는 실패 항목을 반환합니다. 접수 오류는 ID, 원문, 이유, 래핑된 원인을 담는 `*MessageError`이며, 실행이 이미 끝났다면 `ErrRunFinished`와 일치합니다. Stream 전송의 context로 아직 Driver가 가져가지 않은 메시지를 철회할 수 있습니다. Driver가 메시지를 가져간 뒤에는 전달 확인 응답으로 결과가 결정됩니다.

`MessageID`는 Thread 안에서 중복을 제거하며, 전달된 ID를 최대 1000개까지 7일 동안 보관합니다. 재시작 후 복구에는 영속 Thread 저장소가 필요합니다. provider가 메시지를 수락한 뒤 전달 기록이 저장되기 전에 프로세스가 종료되면 메시지가 다시 전달될 수 있습니다. 중복 제거만으로 이 구간의 재전송을 막을 수는 없습니다.

## Standby

각 요청에서 범위가 정해진 하나의 실행 결과가 필요하면 작업 모드(`Thread.Send`)를 사용합니다. 계속 메시지를 받고 답하는 채팅이나 메신저 봇에는 Standby를 사용합니다.

```go
func openChat(serverCtx context.Context, agent *adaptor.Agent) (adaptor.Standby, error) {
	sb, err := agent.Thread("chat-42").Standby(serverCtx)
	if err != nil {
		return nil, err
	}
	events := sb.Events() // subscribe before accepting input
	go func() {
		for event := range events {
			switch e := event.(type) {
			case adaptor.TurnEnd:
				if e.Err == nil && e.Result != nil {
					fmt.Println(e.Result.Text)
				}
			case adaptor.Done:
				if e.Err != nil {
					log.Print(e.Err)
				}
			}
		}
	}()
	return sb, nil
}
```

서버 수명과 같은 context로 Standby를 여세요. HTTP handler에서 `sb.Send(r.Context(), text, adaptor.MessageID(id))`를 호출할 수 있습니다. 이 context는 접수까지만 제한하고, 접수된 작업은 `serverCtx` 아래에서 실행됩니다. 입력은 진행 중인 실행에 합류하거나, 유휴 상태라면 새 실행을 시작합니다. `WithModel` 같은 실행 옵션이 있는 입력은 현재 작업 다음의 별도 실행으로 대기합니다.

Standby에는 `Send`, `Events`, `Cancel`이 있으며, 전체에 대한 `Result()`는 없습니다. provider의 각 턴은 `TurnEnd`를, 각 실행은 `Done`을 발생시킵니다. `sb.Cancel()`은 현재 처리를 중단하고 대기 중인 메시지를 `MessageCancelled`로 실패 처리한 뒤, 새 메시지는 계속 받습니다. Standby를 끝내고 이벤트를 닫으려면 `serverCtx`를 취소하세요. 처음 `Events()`를 호출하기 전의 이벤트는 보관하지 않습니다.

살아 있는 Standby는 유휴 상태에서도 Thread를 소유하므로, 작업 모드의 전송은 `ErrThreadBusy`를 반환합니다. 같은 Agent에서 같은 key로 다시 열면 기존의 살아 있는 핸들을 반환하며, 그 수명은 여전히 첫 번째 context에 속합니다.

실행이 실패해도 Standby는 유지됩니다. 속도 제한, 네트워크, provider 서버 오류로 분류된 실패만 최대 한 번 재시도합니다. 전달되지 않은 `run_failed` 메시지는 같은 ID로 다음 실행에 이월되므로, 관찰자는 실패 후 전달 상태를 볼 수 있습니다. 재시도가 없으면 이월된 입력은 호스트의 다음 메시지를 기다립니다. 전달과 복구의 한계는 [스트리밍 가이드](./docs/streaming.md)를 참고하세요.

## 사람의 승인과 샌드박스

샌드박스 강도, 검색과 브라우저 제어, 승인 모드, 메시지 제한은 모두 `Policy`에서 설정합니다. 생성자가 기본값을 정하고, `Send`가 실행별로 정책 전체를 덮어쓸 수 있습니다.

```go
reviewer := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Sandbox:   adaptor.ReadOnly,
		WebSearch: adaptor.FeatureDeny,
		Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAutoApprove,
		},
	}),
)
```

샌드박스에는 `ReadOnly`, `WorkspaceWrite`, `Unrestricted`의 세 단계가 있습니다. `PolicyReadOnly` 같은 프리셋은 `Sandbox`만 설정하는 단축값입니다. 선택한 Driver가 특정 설정을 지원하지 않으면, 프로세스를 시작하기 전에 명확한 오류를 반환합니다.

승인은 콜백과 이벤트 중 한 방식으로 처리합니다. 콜백 방식은 CLI나 무인 실행에 적합합니다.

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk},
	}),
	adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		switch req.Kind {
		case adaptor.ApprovalPermission:
			return req.Approve(ctx)
		case adaptor.ApprovalQuestion:
			return req.Answer(ctx, "Use PostgreSQL")
		default:
			return req.Deny(ctx, "the plan needs human confirmation")
		}
	}),
)
```

콜백 정책에는 `adaptor.ApproveAll()`과 `adaptor.DenyAll(reason)`을 바로 사용할 수 있습니다. 이미 콜백으로 전달된 요청을 처리하는 함수이며, 지원하지 않는 승인 모드를 활성화하지는 않습니다. Claude와 CodeBuddy는 대화형 요청을 지원합니다. Codex는 app-server를 통해 MCP 도구를 포함한 권한 승인을 지원하지만, 계획 검토나 입력이 필요한 질문은 지원하지 않습니다. Cursor는 `Ask` 지원을 선언하지 않습니다.

콜백을 등록하지 않으면 이벤트 방식을 사용합니다. 요청이 응답 기능을 가진 `*adaptor.ApprovalRequest`로 이벤트 스트림에 도착하므로, 보관했다가 다른 goroutine이나 다른 HTTP 요청에서 처리할 수 있습니다.

```go
func consumeApprovals(ctx context.Context, stream adaptor.Stream,
	publish func(*adaptor.ApprovalRequest)) (*adaptor.Result, error) {
	for event := range stream.Events() {
		if req, ok := event.(*adaptor.ApprovalRequest); ok {
			publish(req) // the host retains the live responder for its UI
		}
	}
	return stream.Result()
}
```

호스트는 별도 엔드포인트에서 `req.Approve(r.Context())`, `req.Deny(r.Context(), reason)`, `req.Answer(r.Context(), text)`를 호출할 수 있습니다. `NoticeApprovalResolved`에는 `Data["request_id"]`가 포함되어 UI에서 대기 중인 카드를 제거할 수 있습니다. 감사 기록을 저장해도 응답 객체 자체가 직렬화되지는 않습니다.

응답은 정확히 한 번만 처리됩니다. 중복 응답, Kind 불일치, 종료된 실행은 각각 안정적인 오류(`ErrApprovalResolved`, `ErrApprovalKindMismatch`, `ErrApprovalExpired`)를 반환하며, 영값 요청도 영구적으로 차단되지 않습니다. 요청이 도착했을 때 `Events()`를 호출한 적이 없고 `OnApproval` handler도 없다면, 생명주기 알림과 함께 시간 초과 대체 처리를 즉시 적용합니다. 그 외에는 기한 안에 응답이 없을 때 `Policy.Approvals`의 `OnTimeout`을 적용하고, 거절에는 `OnReject`를 적용합니다. 대기 중인 요청의 저장 위치는 호스트가 정하며 프로세스 메모리로 한정되지 않습니다.

실행 가능한 웹 HITL 예제는 [`web-chat/copilotkit`](./examples/web-chat/copilotkit)에 있습니다. `/decision/pending`, `/decision/resolve`의 두 엔드포인트를 사용하며, 페이지를 새로 고쳐도 대기 중인 의사 결정이 유지됩니다.

## 여러 턴의 대화

직접 호출하는 `Agent.Send`는 매번 새로운 익명 Thread에서 보내는 것처럼 서로 독립적입니다. 이름 있는 Thread는 기본적으로 Agent 내부의 메모리 저장소를 사용합니다. 저장소를 공유하거나 영속 저장소를 사용하려면 `WithThreadStore`를 지정하세요.

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithThreadStore(memory.NewStore()),
)
defer agent.Close(context.Background())

thread := agent.Thread("tenant-42/issue-123")        // continue the mapped conversation if it exists, otherwise create it
result, err := thread.Send(ctx, "Keep investigating this problem").Result()

only := agent.Thread("tenant-42/issue-123", adaptor.ResumeOnly()) // resume only, never create
branch := thread.Fork("tenant-42/issue-123/plan-b")               // fork from the current progress
```

다음 규칙을 따릅니다.

- **대화 key는 호스트가 정한 문자열입니다.** SDK는 이를 그대로 저장하고 비교합니다. 완전히 새로운 대화에는 새 key를 사용하세요. 기존 key를 새 대화에 다시 연결하는 API는 제공하지 않습니다.
- **하나의 Thread에는 하나의 writer만 있습니다.** lease로 이를 보장합니다. 다른 실행이나 살아 있는 Standby가 있으면 작업 모드의 `thread.Send`는 반환된 Stream을 통해 `ErrThreadBusy`로 실패합니다. 추가 입력은 기존 Stream이나 Standby에 보내세요.
- **이어가기 전에 호환성을 검사합니다.** Driver, 모델, 해석된 실제 workspace, 설정, skills, MCP가 모두 fingerprint에 반영되며, 하나라도 달라지면 잘못된 대화 재사용을 막습니다.
- **실패는 상태를 오염시키지 않습니다.** 0이 아닌 종료 코드, 프로토콜 오류, 취소는 유효한 checkpoint를 만들지 않으므로 이전의 정상적인 대화 기록이 유지됩니다.
- **상주 프로세스는 기본적으로 재사용합니다.** Windows, macOS, Linux에서 Claude, CodeBuddy, Codex는 명시적인 Thread의 턴 사이에 프로세스를 재사용합니다. 특정 턴이나 매 턴에 새 프로세스가 필요하면 `adaptor.WithSpawn()`을 추가하세요. Cursor와 상태 없는 호출은 항상 턴마다 새 프로세스를 시작합니다. `Agent.Close`가 시작된 뒤 새로 보내면 `ErrAgentClosed`를 반환합니다.

단일 프로세스에서는 `memory.NewStore()`를 사용하고, 영속성이 필요하면 `threadstore.Store`를 구현하세요.

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

schema는 Go 타입에서 생성하며, provider의 네이티브 schema 제약을 우선 사용합니다. 현재 transport나 정책이 지원하지 않으면 프롬프트 제약과 로컬 검증으로 자동 전환하며, 둘 다 사용할 수 없을 때만 실행 전에 실패합니다. 타입이 지정된 값과 감사용 전체 `Result`를 함께 반환합니다.

자세한 내용은 [`structured-output` 예제](./examples/structured-output)와 [구조화 출력 문서](./docs/structured-output.md)를 참고하세요.

## 옵션과 리소스

옵션은 하나의 어휘를 공유하며, 적용 범위는 타입으로 컴파일 시점에 구분합니다.

| 타입 | 적용 범위 |
|---|---|
| `Option` | `adaptor.New` 전용 |
| `CallOption` | `Agent.Send`, `Thread.Send`, `Standby.Send` |
| `SharedOption` | 생성과 실행의 기본값. 호출 지점의 값이 생성자의 값을 덮어씁니다. |
| `MessageOption` | `From`, `MessageID`, `When`. `CallOption`으로도 사용하며, `Stream.Send`는 이 옵션만 받습니다. |

병합 규칙은 하나입니다. 가까운 값이 우선하고, skills는 추가하며, 나머지는 각 계약에 따라 교체하거나 병합합니다.

같은 옵션으로 각 에이전트의 주요 설정을 제어합니다.

| 제어할 항목 | 사용할 옵션 |
|---|---|
| 모델 | `WithModel` |
| 시스템 프롬프트 | `WithInstructions` |
| 작업 디렉터리 | `WithWorkspace`, 격리된 worktree에는 `WithWorkspaceSpec` |
| skills | `WithSkills`와 `skill.Dir` / `skill.FS` / `skill.Inline` / `skill.Key` / `skill.Require` |
| MCP | `WithMCP`와 `mcp.Stdio` / `mcp.HTTP` / `mcp.SSE` |
| 샌드박스, 검색, 브라우저 도구, 승인, 메시지 제한 | `WithPolicy`, 대화형 처리에는 `OnApproval` 추가 |
| 설정 디렉터리와 리소스 | `WithProfile`, `WithProfileResources` |
| 시간 제한, 감사 메타데이터, 호출자 identity | `WithTimeout`, `WithMetadata`, `WithIdentity` |
| 대화 영속화 | `WithThreadStore` |

```go
agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithModel("gpt-5.4"),
	adaptor.WithInstructions("You are this repository's reviewer: read the code only, state the conclusion before the evidence."),
	adaptor.WithSkills(skill.Dir("./skills/review")),
	adaptor.WithMCP(mcp.Stdio("repo-tools", "repo-mcp", mcp.Args("serve"))),
	adaptor.WithProfile(profile.Dedicated("./profiles/reviewer")),
	adaptor.WithTimeout(10*time.Minute),
)

result, err := agent.Send(ctx, "Review this change",
	adaptor.WithModel("gpt-5.4-mini"),
	adaptor.WithSkills(skill.Require(skill.Dir("./skills/security"), "this run must pass the security review")), // appends, does not displace the default skills
	adaptor.WithMetadata("request_id", "review-42"),
).Result()
```

같은 설정에 다른 Driver를 사용하면 다른 Agent가 됩니다. Driver가 특정 기능을 지원하지 않으면, 시작 전에 명확한 오류를 반환합니다.

```go
codexReviewer := adaptor.New(codex.Driver(codex.Config{}), reviewerOptions...)
claudeReviewer := adaptor.New(claude.Driver(claude.Config{}), reviewerOptions...)
```

## 호스트 정의 Tools

MCP 서버를 직접 만들고 관리할 필요 없이, 타입이 지정된 Go 함수로 Agent의 기능을 확장합니다.

```go
type SearchInput struct {
	Query string `json:"query" jsonschema:"required"`
}

type SearchOutput struct {
	Files []string `json:"files"`
}

searchRepo := tool.Define(
	"search_repo",
	"Match files using a repository-relative glob.",
	func(ctx context.Context, in SearchInput) (SearchOutput, error) {
		files, err := filepath.Glob(in.Query)
		return SearchOutput{Files: files}, err
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

`WithTools`는 생성 시에만 지정할 수 있으며, Tool 집합 전체를 교체합니다. 기본적으로 handler의 Go 타입에서 schema를 추론하고, 표준 JSON Schema를 명시적으로 제공할 수도 있습니다. `tool.Reject(code, message)`는 모델에 안전하게 보여 줄 수 있는 업무 실패를 나타냅니다. 패키지가 생성한 거절만 그대로 전달하며, 일반 오류, 유사한 오류 값, panic은 정제합니다. 상태 있는 Thread에서 사용하는 모든 Tool에는 `tool.Revision`이 필요합니다. handler 동작의 변경이 이어가기 호환성 검사에 반영되도록 하기 위해서입니다.

여기서 MCP는 내부 전달 수단입니다. 기존 또는 원격 MCP 서버는 계속 `WithMCP`로 연결합니다. 내장 Driver는 SDK가 소유한 격리 profile에 Tools를 물화하며, 사용자가 설정한 네이티브 profile은 변경하지 않습니다. 각 Agent에는 예측할 수 없는 bearer token 환경 변수 이름을 부여하며, 다른 MCP 서버가 같은 인증 정보용 변수를 사용할 수 없게 합니다. 생명주기, schema, 오류, 보안, Thread 의미는 [호스트 정의 Tools 계약](./docs/tools.md)을 참고하세요.

## Agent 격리

`WithProfile`은 Agent가 사용할 provider 설정 디렉터리를 정합니다. `profile.CloneNative`는 로컬 네이티브 설정에서 독립적인 profile을 복제하며, settings, MCP, skills를 선택적으로 가져올 수 있습니다. 로그인 상태는 token을 복사하지 않고 링크로 공유합니다.

```go
worker := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithProfile(profile.CloneNative("/var/agents/worker-1",
		profile.CopySettings(),
		profile.CopyMCP(),
		profile.CopySkills(),
		profile.LinkAuth(), // share the machine's login state via a symlink, so local re-logins are picked up automatically
	)),
)
```

하나의 CLI로 역할별 또는 작업별 인스턴스를 병렬 실행할 수 있습니다. 설정 변경은 인스턴스 사이에서 격리되며, 로컬에서 사용하는 `~/.claude`, `~/.codex`에도 영향을 주지 않습니다.

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

다른 세 가지 선택지도 있습니다. `profile.Native()`는 로컬 네이티브 설정을 직접 사용합니다. `profile.Dedicated(dir)`는 사용자가 관리하는 디렉터리를 지정합니다. `profile.CloneFrom(src, dst, ...)`는 템플릿 디렉터리에서 복제합니다. profile은 대화 fingerprint에 참여하므로 생성 옵션으로만 지정할 수 있고, 호출별로 바꿀 수 없습니다.

선언한 리소스가 실제로 물화되었는지, Driver가 이를 받아들이는지 확인하려면 `agent.ProfileState(ctx)`로 읽고 `agent.SyncProfile(ctx)`로 물화하세요. 둘 다 의도한 리소스와 관찰된 물화 상태를 구분합니다. 전체 흐름은 [`profiles` 예제](./examples/profiles)를 참고하세요.

## 결과와 오류

성공하면 `*Result, nil`을 반환합니다. 실패는 Go의 `error` 경로로만 전달합니다. 실행이 끝났지만 업무적으로 실패한 경우, 사용 가능한 `Result`를 담은 `*RunError`를 반환합니다. 인프라 실패는 일반적인 래핑 가능한 오류입니다.

```go
result, err := agent.Send(ctx, prompt).Result()
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		log.Printf("run failed: %s; available summary: %s", runErr.Reason, runErr.Result.Summary)
	}
	return err
}
```

`Result`의 각 출력 계층은 서로 분리됩니다.

| 필드 | 내용 |
|---|---|
| `Text` | provider의 마지막 턴에서 사용자를 향한 응답 텍스트 |
| `Summary` | 목록, 로그, issue 댓글에 적합한 짧은 요약 |
| `Raw()` | 전체 stdout과 stderr, 마지막으로 인식한 공식 종결 payload |
| `Transcript()` | 모든 턴에서 provider 프로토콜을 파싱한 항목과 core가 기록한 추가 메시지 전달 내역 |
| `Services()` | 이번 실행에서 실제로 관찰한 runtime services |
| `Decode()` | 검증된 구조화 출력 |
| `Turns` / `Usage` | provider 턴 수와 관찰된 사용량의 합계 |
| `Messages()` / `Undelivered()` | 접수 순서의 최종 메시지 결과 / 원문을 포함한 실패 메시지 |
| `Model` / `Provider` / `Metadata` | provider 및 감사 정보 |

`Text`에는 원시 stdout이 섞이지 않으며, 요약이나 provider의 종결 payload가 자동으로 붙지 않습니다. `Done`과 `Stream.Result()`는 같은 결과를 제공합니다. 업무 실패의 사용 가능한 결과는 `RunError.Result`에 담기며, 직접 반환하는 결과 값은 nil입니다.

## 애플리케이션 연동

[AG-UI 1.0 브리지 계약](./docs/agui.md)

**웹 프런트엔드**: 한 줄로 Agent를 AG-UI 프로토콜의 `http.Handler`로 감싸면, CopilotKit 같은 AG-UI 호환 클라이언트가 바로 연결할 수 있습니다.

```go
mux.Handle("/agent", sse.Handler(agent, sse.Options{
	Protocol: sse.AGUI,
}))
```

채팅 UI에서는 `agui.RunRegistry`가 유휴 상태인 Thread에서 실행을 시작하고, 실행 중에는 해당 Stream으로 새 입력을 보냅니다. 기본 Stream.Send의 context 의미를 유지하므로 취소하면 아직 가져가지 않은 입력을 철회할 수 있습니다. CopilotKit 예제는 `POST /message`의 전송을 추가 요청의 context와 명시적으로 분리합니다. 전송이 시작된 뒤에는 HTTP 연결이 끊겨도 입력을 철회하거나 큐 대기를 끝내지 않으며, 전달 확인은 원래 SSE 스트림으로 도착합니다. 선택적으로 사용하는 `sessionrecorder`는 기본적으로 안전한 최종 필드를 기록하고, 추가 진단 정보는 `WithExposure`로 명시적으로 활성화합니다. 라우팅과 기록에 대해서는 [스트리밍 가이드](./docs/streaming.md#8-ag-ui-bridge)를 참고하세요.

**A2A**: `bridges/a2a`는 임의의 Runner를 A2A 서버로 노출하고, 라우팅, 인증, TLS는 호스트에 맡깁니다.

```go
server := bridgea2a.NewServer(agent, bridgea2a.ServerOptions{
	AgentCard: bridgea2a.AgentCard{
		Name:        "Local coding agent",
		Description: "Runs coding tasks through agent-adaptor",
		Version:     "2.0.0",
		URL:         "https://host.example/a2a",
	},
	Session: bridgea2a.ThreadByContextID(), // the remote contextID maps stably to a local Thread key
	Options: []adaptor.CallOption{adaptor.WithPolicy(adaptor.PolicyWorkspaceWrite)},
})

mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
mux.Handle("/a2a", server.Handler())
```

원격 A2A 에이전트를 호출할 때는 `clients/a2a`를 사용합니다. A2A의 task, message, artifact를 반환하며, 원격 프로토콜 작업을 로컬 CLI의 stdout이나 `Result`처럼 취급하지 않습니다.

```go
client := clienta2a.New(clienta2a.Options{
	AgentCardURL: "https://remote.example/.well-known/agent-card.json",
	Auth:         clienta2a.BearerTokenFromEnv("REMOTE_A2A_TOKEN"),
})
defer client.Close()

task, err := client.Send(ctx, clienta2a.SendRequest{
	Message: clienta2a.Message{
		Role:  "user",
		Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "Review this change"}},
	},
})
```

중간 과정이 필요하면 `SendStream` / `Subscribe`를 사용하세요. 추론, 도구 호출, 승인 이벤트, 진단 필드를 외부에 노출할지는 `ExposurePolicy`로 제어하며, 기본값은 최소 노출입니다. 노출 제어, Remote 멤버, 프로토콜의 한계는 [A2A 연동](./docs/a2a.md)을 참고하세요.

## 멀티 에이전트 협업

[`hosttools/team`](./hosttools/team)은 위임 중심의 leader에 호스트가 선택한 로컬 Agent, 중첩된 Team, Remote A2A 멤버를 연결합니다. 로컬 멤버는 서로 다른 Driver를 사용할 수 있습니다. leader에는 고정 도구 네 개가 제공됩니다. worker는 leader를 통해 보고하며, 작업 및 멤버 업데이트와 로컬 worker의 승인 요청은 하나의 Event 스트림에 모입니다.

```go
func Coordinate(ctx context.Context, leader, planner, implementer *adaptor.Agent) (*adaptor.Result, error) {
	t, err := team.New(team.Config{
		Leader: leader,
		Members: []team.Member{
			{Key: "plan", Description: "Plan the change", Runner: planner},
			{Key: "impl", Description: "Implement and test", Runner: implementer},
		},
	})
	if err != nil {
		return nil, err
	}
	defer t.Close(context.Background())
	return t.Thread("issue-123").Send(ctx, "Plan and implement TASK.md").Result()
}
```

`context`, `adaptor "github.com/agent-dance/agent-adaptor/v2"`, `"github.com/agent-dance/agent-adaptor/v2/hosttools/team"`을 import하세요. Agent의 소유권은 호스트에 남습니다. 지속적인 입력에는 `Thread(key).Standby(ctx)`를 사용하고, 사용 중인 scope에 `Thread.Send`를 호출하면 `ErrThreadBusy`를 반환합니다. [팀 가이드](./docs/team.md)에는 Remote 멤버, 도구, 승인, 제한, 영속 복구, 지연 Fork를 설명합니다.

[라이브 쇼케이스](./examples/showcases/team-agent-workflow)는 역할별 샌드박스와 CopilotKit 페이지를 사용해 로컬의 계획 → 구현 → 리뷰 흐름을 실행합니다.

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
```

## 환경 프로브

`Agent.Inspect()`는 실행 전 점검, 환경 진단, 모델 선택을 위한 읽기 전용 프로브입니다. 지원하지 않는 프로브는 데이터를 만들어 내지 않고 unsupported를 명시적으로 반환합니다.

```go
environment, err := agent.Inspect().Environment(ctx) // health, CLI version, and effective message capability
models, err := agent.Inspect().Models(ctx)
quota, err := agent.Inspect().Quota(ctx)
state, err := agent.ProfileState(ctx)                // reports desired versus observed only, changes nothing
synced, err := agent.SyncProfile(ctx)                // explicitly materializes configuration resources
```

내장 환경 프로브는 `Version`과 `Messages`를 보고합니다. 감지한 CLI 버전이 메시지 기능의 최소 버전보다 낮으면 실행 중 전달을 비활성화하고, 대기 중인 메시지는 checkpoint에서 이어가기로 전달하며, 실행에서 Notice를 한 번 발행합니다. 버전을 확인할 수 없으면 선언된 기능을 유지하고 실행 시 대체 경로를 사용합니다. [Driver 지원 표](./docs/streaming.md#12-message-delivery-capabilities)를 참고하세요.

## 여섯 개의 명사

라이브러리의 전체 공개 모델은 여섯 개의 명사로 구성됩니다.

| 명사 | 의미 |
|---|---|
| `Agent` | 설정을 마쳐 생성 직후 실행할 수 있는 에이전트 |
| `Thread` | 호스트 key로 식별하며 이어가기와 분기가 가능한 대화 |
| `Stream` | 진행 중인 하나의 실행 |
| `Event` | 실행 중 발생한 하나의 타입이 지정된 이벤트 |
| `Result` | 한 실행의 최종 결과와 감사 정보 |
| `Driver` | 에이전트 CLI 하나의 연동 구현. 확장 작성자용 인터페이스 |

이에 따라 생성자 하나, 옵션 병합 규칙 하나, 실행 파이프라인 하나, 이벤트 스트림 하나, 실패 판정 경로 하나를 유지합니다.

## 패키지

| 패키지 | 용도 |
|---|---|
| [`driver`](./driver) | 새 에이전트를 연동할 때 사용하는 Driver SPI |
| [`codex`](./codex), [`claude`](./claude), [`cursor`](./cursor), [`codebuddy`](./codebuddy) | 내장 Driver와 각자의 Config 타입 |
| [`tool`](./tool), [`skill`](./skill), [`mcp`](./mcp), [`profile`](./profile) | SDK 사용자를 위한 기능 및 리소스 어휘 |
| [`threadstore`](./threadstore), [`memory`](./memory) | Thread 영속화 계약과 메모리 구현 |
| [`bridges`](./bridges) | SSE, AG-UI, A2A, subagent-stream 프로토콜 브리지 |
| [`clients/a2a`](./clients/a2a) | A2A 클라이언트 |
| [`hosttools`](./hosttools) | 선택적인 위임 조율 및 이벤트 기록 컴포넌트 |
| [`adaptertest`](./adaptertest) | Driver 일관성 테스트 스위트 |

자체 에이전트 CLI를 연동하려면 `driver.Driver`를 구현하고 `adaptertest`를 통과시키세요. 이후에는 내장 Driver와 같은 상위 기능을 사용할 수 있습니다.

## 예제

- [`quickstart`](./examples/quickstart): Agent를 생성하고 프롬프트 하나를 실행합니다.
- [`streaming`](./examples/streaming): 이벤트 소비, 선택적인 시간 기반 취소, 최종 텍스트 대체 출력을 보여 줍니다. [`streaming/chat`](./examples/streaming/chat)은 이름 있는 Thread에서 작업 하나를 스트리밍합니다.
- [`threads`](./examples/threads): 이어가기, 이어가기 전용 모드, 분기, checkpoint 감사를 보여 줍니다.
- [`structured-output`](./examples/structured-output): 타입이 지정된 JSON 출력입니다.
- [`tools`](./examples/tools): MCP를 직접 관리하지 않고 타입이 지정된 Go 함수를 실제 로컬 provider에 노출합니다.
- [`skills`](./examples/skills): skill을 선택하고 증거 파일을 확인합니다. [`profiles`](./examples/profiles)는 profile 리소스를 동기화하며, 선택적으로 로컬 프로브나 작업 전송을 수행합니다.
- [`inspect`](./examples/inspect): 실제 호출 두 번을 실행한 뒤 환경 및 모델 프로브, 선언된 reviewer skill 선택, profile 관찰을 수행합니다.
- [`web-chat`](./examples/web-chat): [`aguiclient`](./examples/web-chat/aguiclient), [`copilotkit`](./examples/web-chat/copilotkit) 프런트엔드가 포함된 SSE/AG-UI 서버입니다. [`assistant-ui`](./examples/web-chat/assistant-ui)는 영속 기록, 도구 실패 표시, 선택적 실행 관찰을 제공한다(기본 fake).
- [`a2a-server`](./examples/a2a-server): Agent를 A2A로 노출하고 호출합니다.
- [`showcases/team-agent-workflow`](./examples/showcases/team-agent-workflow): 계획, 구현, 리뷰를 하나의 파이프라인으로 연결합니다.

실제 호출이 있는 예제는 해당 CLI와 로그인 상태가 필요합니다. 저장소의 일반 테스트는 유료 호출을 발생시키지 않습니다.

## 경계

core 라이브러리는 범용 HTTP/gRPC 서버, 호스트 작업 큐, 스케줄러, 멀티테넌시, 인증, 데이터베이스를 제공하지 않으며, 작업을 어느 에이전트에 보낼지도 결정하지 않습니다. 프로토콜 서비스는 bridges와 호스트 애플리케이션이, 팀 역할과 워크플로 정책은 호스트가 담당합니다.

## 문서

- [문서 맵](./docs/README.md)
- [API reference](./docs/api-reference.md)
- [호스트 정의 Tools](./docs/tools.md)
- [스트리밍 가이드](./docs/streaming.md)
- [구조화 출력](./docs/structured-output.md)
- [실행 정책: 샌드박스, 승인, 시간 제한](./docs/run-policy.md)
- [A2A 연동](./docs/a2a.md)
- [공개 오류](./docs/public-errors.md)

## v1에서 v2로 마이그레이션

모든 SDK import와 의존성에 `/v2` 모듈 경로를 사용하세요.

| v1 | v2 |
|---|---|
| `agent.Run(ctx, prompt, opts...)` | `agent.Send(ctx, prompt, opts...).Result()` |
| `agent.Stream(ctx, prompt, opts...)` | `agent.Send(ctx, prompt, opts...)` |
| `thread.Run` / `thread.Stream` | `thread.Send` 후 `Result()` 또는 `Events()` |
| 실행 메서드 두 개를 가진 `Runner` | `Send`만 가진 `Runner` |
| `RunFinished`를 완료로 취급 | 최종 결과를 판정하는 `Done`을 읽거나 `Result()` 호출 |
| Thread에 명시적인 저장소 필요 | 기본값은 Agent 내부 메모리이며, 재시작 후 복구에는 영속 저장소 지정 |

`RunAs[T]`는 동기식 typed helper로 유지되며 내부에서 `Send`를 사용합니다. 일반 진행 이벤트를 읽으려면 먼저 `Events()`를 구독하세요. Result만 읽는 호출도 나중에 읽을 수 있도록 핵심 이벤트와 누락 요약을 보관합니다. 추가 입력은 `Stream.Send`, 지속적인 채팅은 `Thread.Standby(serverCtx)`를 사용합니다. `Agent.Close(ctx)`는 종료 시 Agent가 소유한 리소스를 해제합니다.

## 라이선스

별도로 명시하지 않는 한 이 저장소는 [Apache License, Version 2.0](./LICENSE)에 따라 배포됩니다. 제3자 자료에는 각 자료의 라이선스와 저작자 표시가 그대로 적용됩니다. [제3자 고지](./THIRD_PARTY_NOTICES.md)를 참고하세요.

Codex, Claude, Cursor, CodeBuddy 및 기타 제품명은 각 소유자의 상표입니다. 이 명칭은 지원하는 연동을 식별하기 위한 용도로만 사용하며, 이 프로젝트는 해당 소유자와 제휴 관계가 없고 보증을 받지 않습니다.
