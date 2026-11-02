# agent-adaptor

[English](./README.md) | [简体中文](./README.zh-CN.md) | [한국어](./README.ko.md) | [Deutsch](./README.de.md)

`agent-adaptor` v2 は、`Codex`、`Claude Code`、`Cursor`、`CodeBuddy` などのコーディング Agent を、小さく直感的な共通 API で操作する SDK です。単なる呼び出しにとどまらず、会話管理や各種の拡張機能も提供します。

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
result, err := agent.Send(ctx, "Fix the failing tests").Result()
```

Claude Code に切り替えるには、コンストラクタの Driver を差し替えるだけです。ほかのコードは変更する必要がありません。

## 機能概要

- **統一設定**：共通 API で、各 Agent の skills、MCP、システムプロンプト、モデル、sandbox、ツール、承認を制御します。
- **ストリーミング応答**：必要に応じて出力をストリーミングで受け取り、思考、テキスト、ツール呼び出し、意思決定リクエストを区別できます。
- **会話管理**：会話の継続と分岐に対応します。チケット番号やユーザー ID など、ホスト側の業務 ID を会話キーとして使えるため、provider 固有のセッション管理を意識する必要がありません。
- **人による意思決定**：コールバックやイベントを通じて質問に答え、危険なコマンドを止め、計画を承認できます。ホストは有効な responder を保持し、別の HTTP リクエストから応答できます。承認の監査記録をどこに保存するかはホストが決めます。

## 高度な機能

- **構造化出力**：Go の構造体を定義し、`RunAs[T]` を呼ぶと、Agent はその構造に従って実行し、値が設定されたオブジェクトを返します。
- **プロトコル対応**：組み込みの A2A/AGUI ラッパーにより、1 行で Agent を SSE + AGUI ストリーミング対応にできます。独自のフロントエンドやクライアントを接続すれば Agent サービスを構成できます。実行可能な CopilotKit フロントエンドの例も含まれています。
- **チーム**：ホストが選んだ leader が、任意の Driver を使うメンバーや Remote A2A メンバーを調整します。たとえば Codex が計画担当（Codex）、実装担当（Claude）、レビュー担当（Cursor）をまとめ、タスクの更新と worker の承認要求を leader の Event ストリームに集約します。[チームガイド](./docs/team.md)と examples/showcases/team-agent-workflow を参照してください。
- **Agent の分離**：マシン上の Agent 設定を専用ディレクトリに複製し、ログイン状態をリンクで共有することで、設定変更を分離します。並行開発や役割分担のために、複数の Codex/Claude Code インスタンスを同時に実行できます。

## インストール

```bash
go get github.com/agent-dance/agent-adaptor/v2
```

Go 1.26.5 以降が必要です。

**実行時には、対応する Agent CLI がインストール済みで、認証も完了している必要があります。**

## クイックスタート

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

4 つの組み込み Driver は同じ方法で構築します。それぞれ独自の `Config` を持ちます。

```go
codexAgent := adaptor.New(codex.Driver(codex.Config{}))
claudeAgent := adaptor.New(claude.Driver(claude.Config{}))
cursorAgent := adaptor.New(cursor.Driver(cursor.Config{}))
codeBuddyAgent := adaptor.New(codebuddy.Driver(codebuddy.Config{}))
```

## ストリーミング実行

`Send` は 1 回の実行を開始し、`Stream` を返します。`Events()` から進捗を読み取るか、`Result()` を呼んで同期的に完了を待ちます。

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

テキスト、思考、ツール呼び出しとその結果、プロセス情報、ライフサイクル、メッセージの配送結果、承認要求はすべてこの 1 本のストリームを流れます。provider の各ターンは `TurnEnd` を発行します。最後のイベントは `Done{Result, Err}` です。検証、checkpoint の処理、リソースの解放が終わってから発行され、`stream.Result()` と同じ結果ポインタおよびエラーを持ちます。`RunFinished` は参考情報です。

途中でイベントの読み取りをやめる場合は `Cancel()` を呼んでください。何度呼んでも安全です。

## 実行中のメッセージ

返された Stream に追加の指示を送れます。重複排除には、ホスト側で安定した ID を割り当てます。

```go
stream := agent.Send(ctx, "Fix the failing tests")
err := stream.Send(ctx, "Keep the public API unchanged",
	adaptor.MessageID("review-comment-42"),
	adaptor.From(adaptor.Author{Kind: adaptor.AuthorHuman, ID: "reviewer"}),
	adaptor.When(adaptor.Soon),
)
```

既定の `Soon` は、対応する最も早い境界で配送するか、ターン終了後に継続します。`AfterTurn` は継続ターンを待ちます。`Interrupt` は進行中のターンを中断し、同じ実行内で継続します。未対応の Driver は `ErrMessageUnsupported` で拒否します。[Driver の対応表](./docs/streaming.md#12-message-delivery-capabilities)を参照してください。

送信が nil エラーを返すのは、メッセージを受け付けたという意味です。`MessageUpdate` は受付、配送成功、失敗を報告し、失敗時には元のテキストを保持します。`Result.Messages()` は最終結果を記録し、`Undelivered()` は失敗したメッセージを返します。受付時のエラーは `*MessageError` で、ID、テキスト、理由、ラップした原因を含みます。実行済みの場合は `ErrRunFinished` に一致します。Stream に送る際の context は、まだ Driver に取得されていないメッセージを取り消せます。Driver が取得した後は、配送の確認応答によって結果が決まります。

`MessageID` は Thread 内で重複を排除し、配送済み ID を最大 1000 件、7 日間保持します。再起動後の復旧には永続的な Thread store が必要です。provider が受け付けてから配送確認を保存するまでの間にクラッシュすると、メッセージが再配送される場合があります。重複排除はこのクラッシュの隙間を解消するものではありません。

## Standby

リクエストごとに区切られた実行結果が必要な場合は、タスクモード（`Thread.Send`）を使います。継続的にメッセージを受け取って返信するチャットや IM bot には Standby を使います。

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

Standby はサーバーのライフサイクルに対応する context で開始します。HTTP handler からは `sb.Send(r.Context(), text, adaptor.MessageID(id))` を呼べます。この context が制限するのは受付までで、受付後の処理は `serverCtx` に属します。入力は進行中の実行に加わり、待機中なら新しい実行を開始します。`WithModel` などの実行オプションを指定すると、現在の処理の後に別の実行としてキューに入ります。

Standby が持つのは `Send`、`Events`、`Cancel` で、全体をまとめる `Result()` はありません。provider の各ターンは `TurnEnd` を、各実行は `Done` を発行します。`sb.Cancel()` は現在の処理を止め、待機中のメッセージを `MessageCancelled` で失敗させた後、引き続き入力を受け付けます。Standby 自体を終了してイベントを閉じるには `serverCtx` をキャンセルします。最初の `Events()` 呼び出し前に発生したイベントは破棄されます。

有効な Standby は待機中も Thread の所有権を保持し、タスクモードの送信は `ErrThreadBusy` を返します。同じ Agent で同じキーを再び開くと、既存の有効なハンドルが返ります。そのライフサイクルは引き続き最初の context に属します。

実行が失敗しても Standby は終了しません。レート制限、ネットワーク、provider のサーバー障害として分類された失敗だけを、最大 1 回再試行します。未配送の `run_failed` メッセージは同じ ID のまま次の実行へ引き継がれるため、観測側は失敗の後に配送成功を見ることがあります。再試行しない場合、引き継いだ入力はホストからの次のメッセージを待ちます。配送と復旧の制約は[ストリーミングガイド](./docs/streaming.md)を参照してください。

## 人による承認と sandbox

sandbox の強度、検索とブラウザーの制御、承認モード、メッセージの上限は、同じ `Policy` にまとめられています。コンストラクタで既定値を設定し、`Send` で実行ごとにポリシー全体を上書きできます。

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

sandbox には `ReadOnly`、`WorkspaceWrite`、`Unrestricted` の 3 段階があります。`PolicyReadOnly` などのプリセットは `Sandbox` だけを設定するショートカットです。選択した Driver がいずれかの設定に対応していない場合、プロセス開始前に明示的なエラーを返します。黙って制約を緩めることはありません。

承認には 2 つの受け取り方があります。どちらかを選んでください。コールバックを登録する方式は、CLI や無人実行に適しています。

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

コールバック方式では `adaptor.ApproveAll()` と `adaptor.DenyAll(reason)` を利用できます。これらはコールバックに届いた要求を処理するもので、未対応の承認モードを有効にはしません。Claude と CodeBuddy は対話的な要求に対応します。Codex は app-server を通じて MCP ツールを含む Permission の承認に対応しますが、PlanReview や入力が必要な Question には対応していません。Cursor は `Ask` 対応を宣言していません。

コールバックを登録しない場合、要求は responder を持つ `*adaptor.ApprovalRequest` としてイベントストリームに届きます。ホストは要求を保留し、後から任意の goroutine や別の HTTP リクエストで解決できます。Web アプリケーションで使いやすい方式です。

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

ホストは別のエンドポイントから `req.Approve(r.Context())`、`req.Deny(r.Context(), reason)`、`req.Answer(r.Context(), text)` を呼べます。`NoticeApprovalResolved` は `Data["request_id"]` を持つため、UI は解決済みの保留カードを閉じられます。監査記録を保存しても、有効な responder がシリアライズされるわけではありません。

応答は 1 回だけ受理されます。重複した応答、Kind の不一致、実行終了後の応答には、安定したエラー（`ErrApprovalResolved`、`ErrApprovalKindMismatch`、`ErrApprovalExpired`）を返します。ゼロ値の要求が永久にブロックすることもありません。要求の到着時に `Events()` が一度も呼ばれず、`OnApproval` handler もなければ、タイムアウト時のフォールバックをすぐ適用し、ライフサイクルの Notice を発行します。それ以外は、期限までに応答がない場合に `Policy.Approvals` の `OnTimeout` に従い、拒否された場合は `OnReject` に従います。保留要求の保存先はホストが決め、プロセスメモリに限定されません。

実行可能な Web HITL の一式は [`web-chat/copilotkit`](./examples/web-chat/copilotkit) にあります。`/decision/pending` と `/decision/resolve` の 2 つのエンドポイントを備え、ページを再読み込みしても保留中の意思決定を表示できます。

## 複数ターンの会話

`Agent.Send` を直接呼ぶと、毎回新しい匿名 Thread に送るのと同じく、互いに独立した実行になります。名前付き Thread は、既定では Agent ごとのメモリ store を使います。store を共有する場合や永続化する場合は `WithThreadStore` を指定します。

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

主な規則は次のとおりです。

- **会話キーはホストの文字列です。** SDK はそのまま保存して比較します。新しい会話には新しいキーを使います。既存のキーを新しい会話に割り当て直す API はありません。
- **1 つの Thread の書き手は 1 つです。** lease によって保証します。別の実行や有効な Standby がある場合、タスクモードの `thread.Send` は Stream を通じて `ErrThreadBusy` を返します。追加入力は既存の Stream または Standby に送ります。
- **継続前に互換性を検証します。** Driver、モデル、解決済み workspace、設定、skills、MCP はすべて fingerprint に含まれます。いずれかが変わると、不適切な会話の再利用を防ぎます。
- **失敗は保存状態を汚染しません。** 0 以外の終了コード、プロトコルエラー、キャンセルからは有効な checkpoint を生成せず、以前の正常な会話記録を維持します。
- **常駐プロセスを既定で再利用します。** Windows、macOS、Linux では、Claude、CodeBuddy、Codex が明示的な Thread のターン間で同じプロセスを再利用します。特定のターン、または毎ターン新しいプロセスが必要なら `adaptor.WithSpawn()` を指定します。Cursor とステートレス呼び出しは、常にターンごとに新しいプロセスを開始します。`Agent.Close` の開始後に送信すると `ErrAgentClosed` を返します。

単一プロセスでは `memory.NewStore()` を使えます。永続化が必要な場合は `threadstore.Store` を実装してください。

## 構造化出力

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

schema は Go の型から生成され、provider が備えるネイティブの schema 制約を優先します。現在の transport や policy が対応していない場合は、プロンプトによる制約とローカル検証に自動でフォールバックします。どちらも利用できない場合に限り、開始前に失敗します。戻り値には型付きの値と、完全な監査情報を持つ `Result` の両方が含まれます。

詳しくは [`structured-output` の例](./examples/structured-output)と[構造化出力のドキュメント](./docs/structured-output.md)を参照してください。

## オプションとリソース

オプションの語彙は共通で、適用範囲を型によってコンパイル時に区別します。

| 型 | 適用先 |
|---|---|
| `Option` | `adaptor.New` のみ |
| `CallOption` | `Agent.Send`、`Thread.Send`、`Standby.Send` |
| `SharedOption` | 構築時と実行時の既定値。呼び出し時の指定がコンストラクタを上書きする |
| `MessageOption` | `From`、`MessageID`、`When`。`CallOption` としても使え、`Stream.Send` が受け付けるのはこの種類のみ |

マージの規則は 1 つです。近い指定を優先し、skills は追加し、それ以外はそれぞれの契約に従って置換またはマージします。

同じオプション群で、各 Agent の主な設定を制御できます。

| 制御したいもの | 使用するもの |
|---|---|
| モデル | `WithModel` |
| システムプロンプト | `WithInstructions` |
| 作業ディレクトリ | `WithWorkspace`、分離した worktree には `WithWorkspaceSpec` |
| skills | `WithSkills` と `skill.Dir` / `skill.FS` / `skill.Inline` / `skill.Key` / `skill.Require` |
| MCP | `WithMCP` と `mcp.Stdio` / `mcp.HTTP` / `mcp.SSE` |
| sandbox、検索、ブラウザーツール、承認、メッセージ上限 | `WithPolicy`、対話的な場合は `OnApproval` も指定 |
| 設定ディレクトリとリソース | `WithProfile`、`WithProfileResources` |
| タイムアウト、監査メタデータ、呼び出し元 identity | `WithTimeout`、`WithMetadata`、`WithIdentity` |
| 会話の永続化 | `WithThreadStore` |

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

同じ設定でも Driver を変えれば別の Agent になります。ある能力を Driver がサポートしない場合は、開始前に明示的なエラーを返します。

```go
codexReviewer := adaptor.New(codex.Driver(codex.Config{}), reviewerOptions...)
claudeReviewer := adaptor.New(claude.Driver(claude.Config{}), reviewerOptions...)
```

## ホスト定義の Tools

MCP server を自分で構築、管理することなく、型付きの Go 関数で Agent の能力を直接拡張できます。

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

`WithTools` は構築時専用で、Tool 集合全体を置換します。schema は既定で handler の Go 型から推論され、標準 JSON Schema を明示的に指定することもできます。`tool.Reject(code, message)` は、モデルに安全に見せられる業務エラーを報告します。この境界を通過するのはパッケージが生成した拒否だけで、通常のエラー、似た形の値、panic は詳細を除去されます。状態を持つ Thread で使うすべての Tool に `tool.Revision` が必要です。これにより handler の振る舞いの変更が、継続時の互換性判定に含まれます。

ここでの MCP は内部の配送手段です。既存またはリモートの MCP server は引き続き `WithMCP` を使います。組み込み Driver は SDK が所有する分離済み profile に Tools を展開するため、指定されたネイティブ profile は変更しません。各 Agent には予測不可能な名前の bearer token 環境変数が割り当てられ、ほかの MCP server が同じ認証情報の変数名を使うことはできません。ライフサイクル、schema、エラー、セキュリティ、Thread の意味論は[ホスト定義 Tools の契約](./docs/tools.md)を参照してください。

## Agent の分離

`WithProfile` は Agent が使う provider の設定ディレクトリを決めます。`profile.CloneNative` は、マシン上のネイティブ設定から独立した profile を複製し、必要に応じて設定、MCP、skills を引き継ぎます。ログイン状態は token をコピーせず、リンクで共有します。

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

そのため、1 つの CLI で役割別、タスク別に複数のインスタンスを並行実行できます。設定変更は相互に分離され、普段使う `~/.claude` や `~/.codex` にも影響しません。

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

ほかに 3 つの選択肢があります。`profile.Native()` はマシン上のネイティブ設定を直接使い、`profile.Dedicated(dir)` は自分で管理するディレクトリを固定し、`profile.CloneFrom(src, dst, ...)` はテンプレートディレクトリから派生させます。profile は会話の fingerprint に含まれるため、構築時にだけ指定でき、呼び出しごとには切り替えられません。

宣言したリソースのどれが実際に展開され、Driver に認識されているかを確認するには、`agent.ProfileState(ctx)` で読み取り、`agent.SyncProfile(ctx)` で展開します。両者とも、要求されたリソースと実際に観測した展開状態を区別します。全体の流れは [`profiles` の例](./examples/profiles)を参照してください。

## 結果とエラー

成功すると `*Result, nil` を返します。失敗は Go の `error` だけで伝えます。処理が終了しても業務上は失敗した場合、取得できた `Result` を含む `*RunError` を返します。インフラの失敗は通常のラップ可能なエラーです。

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

`Result` の各出力層は、ほかの層と分離されています。

| フィールド | 内容 |
|---|---|
| `Text` | provider の最後のターンが返した、ユーザー向けの応答テキスト |
| `Summary` | 一覧、ログ、issue コメントに適した短い要約 |
| `Raw()` | 完全な stdout と stderr、および最後に認識した公式の終端 payload |
| `Transcript()` | 全ターンの provider 解析項目と、core が記録した追加入力の配送 |
| `Services()` | 今回の実行で実際に観測した runtime service |
| `Decode()` | 検証済みの構造化出力 |
| `Turns` / `Usage` | provider のターン数と、観測した使用量の合計 |
| `Messages()` / `Undelivered()` | 受付順の最終メッセージ結果 / 元のテキストを含む失敗メッセージ |
| `Model` / `Provider` / `Metadata` | provider 情報と監査情報 |

`Text` に生の stdout が混ざることはなく、要約や provider の終端 payload が自動で追加されることもありません。`Done` と `Stream.Result()` は同じ結果を公開します。業務上の失敗では、取得できた結果は `RunError.Result` に入り、直接の result 戻り値は nil になります。

## アプリケーションへの組み込み

**Web フロントエンド**：1 行で Agent を AG-UI プロトコルの `http.Handler` に包めるため、CopilotKit などの AG-UI 対応クライアントから直接接続できます。

```go
mux.Handle("/agent", sse.Handler(agent, sse.Options{
	Protocol: sse.AGUI,
}))
```

チャット UI では、`agui.RunRegistry` が待機中の Thread で実行を開始し、実行中はその Stream に新しい入力を送ります。context は元の Stream.Send と同じ扱いで、キャンセルにより未取得の入力を取り消せます。CopilotKit の例では `POST /message` の送信処理を補足リクエストの context から明示的に切り離します。送信開始後は HTTP 切断で入力が撤回されたりキュー待ちが終了したりせず、配送確認は元の SSE ストリームに届きます。任意で利用できる `sessionrecorder` は、既定では安全な終局フィールドを記録し、追加の診断情報は `WithExposure` で明示的に有効にします。ルーティングと記録については[ストリーミングガイド](./docs/streaming.md#8-ag-ui-bridge)を参照してください。

**A2A**：`bridges/a2a` は任意の Runner を A2A server として公開します。ルーティング、認証、TLS はホストが担当します。

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

リモートの A2A Agent を呼び出すには `clients/a2a` を使います。戻り値は A2A の task、message、artifact です。リモートのプロトコルタスクに、ローカル CLI の stdout や `Result` があるかのようには扱いません。

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

途中の経過が必要なら `SendStream` / `Subscribe` を使います。思考、ツール呼び出し、承認イベント、診断フィールドを外部に公開するかどうかは `ExposurePolicy` が制御し、既定では最小限のみを公開します。公開範囲の制御、Remote メンバー、プロトコルの制約は [A2A 連携](./docs/a2a.md)を参照してください。

## 複数 Agent の協調

[`hosttools/team`](./hosttools/team) は、委任を中心に動く leader と、ホストが選んだローカル Agent、入れ子の Team、Remote A2A メンバーを組み合わせます。ローカルメンバーは異なる Driver を使えます。leader には 4 つの固定ツールが与えられ、worker は leader に報告します。タスクとメンバーの更新、およびローカル worker の承認要求は、1 本の Event ストリームで共有されます。

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

`context`、`adaptor "github.com/agent-dance/agent-adaptor/v2"`、`"github.com/agent-dance/agent-adaptor/v2/hosttools/team"` を import します。Agent の所有権はホストが保持します。継続的な入力には `Thread(key).Standby(ctx)` を使います。処理中の scope に対する `Thread.Send` は `ErrThreadBusy` を返します。[チームガイド](./docs/team.md)では、Remote メンバー、ツール、承認、上限、永続ストアからの復旧、遅延 Fork を説明しています。

[ライブ実行のショーケース](./examples/showcases/team-agent-workflow)は、役割ごとの sandbox と CopilotKit ページを備え、ローカルで計画 → 実装 → レビューのワークフローを実行します。

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
```

## 環境プローブ

`Agent.Inspect()` は、起動前の確認、環境診断、モデル選択のための読み取り専用プローブです。未対応のプローブは明示的に unsupported を返し、データを捏造しません。

```go
environment, err := agent.Inspect().Environment(ctx) // health, CLI version, and effective message capability
models, err := agent.Inspect().Models(ctx)
quota, err := agent.Inspect().Quota(ctx)
state, err := agent.ProfileState(ctx)                // reports desired versus observed only, changes nothing
synced, err := agent.SyncProfile(ctx)                // explicitly materializes configuration resources
```

組み込みの環境プローブは `Version` と `Messages` を報告します。検出した CLI がメッセージ配送に必要な最小バージョンを下回る場合、実行中の配送を無効にし、待機中のメッセージは checkpoint からの resume で継続します。このとき実行は Notice を 1 回発行します。バージョンが不明な場合は宣言済みの能力を維持し、実行時のフォールバックに従います。[Driver の対応表](./docs/streaming.md#12-message-delivery-capabilities)を参照してください。

## 6 つの名詞

ライブラリの公開モデルは、次の 6 つの名詞で構成されます。

| 名詞 | 意味 |
|---|---|
| `Agent` | 設定が完了し、構築後すぐ実行できる Agent |
| `Thread` | ホストのキーで識別される、継続と分岐が可能な会話 |
| `Stream` | 進行中の 1 回の実行 |
| `Event` | 実行中に発生した、型を持つ 1 つの出来事 |
| `Result` | 1 回の実行の最終結果と監査情報 |
| `Driver` | Agent CLI の接続実装。拡張の作者が扱う |

これに対応する制約は、コンストラクタを 1 つ、オプションのマージ規則を 1 つ、実行パイプラインを 1 本、イベントストリームを 1 本、失敗の判定経路を 1 つにすることです。

## パッケージ

| パッケージ | 用途 |
|---|---|
| [`driver`](./driver) | 新しい Agent を接続するための Driver SPI |
| [`codex`](./codex)、[`claude`](./claude)、[`cursor`](./cursor)、[`codebuddy`](./codebuddy) | 組み込み Driver と、それぞれの Config 型 |
| [`tool`](./tool)、[`skill`](./skill)、[`mcp`](./mcp)、[`profile`](./profile) | SDK 利用者向けの能力とリソースの語彙 |
| [`threadstore`](./threadstore)、[`memory`](./memory) | Thread の永続化契約とメモリ実装 |
| [`bridges`](./bridges) | SSE、AG-UI、A2A、subagent-stream のプロトコルブリッジ |
| [`clients/a2a`](./clients/a2a) | A2A クライアント |
| [`hosttools`](./hosttools) | 任意で使える委任調整とイベント記録のコンポーネント |
| [`adaptertest`](./adaptertest) | Driver の適合性テストスイート |

独自の Agent CLI を接続するには、`driver.Driver` を実装し、`adaptertest` を通過させます。その後は、組み込み Driver と同じ上位機能を利用できます。

## 例

- [`quickstart`](./examples/quickstart)：Agent を構築し、1 つのプロンプトを実行します。
- [`streaming`](./examples/streaming)：イベントの読み取り、任意の時間指定キャンセル、最終テキストへのフォールバック。[`streaming/chat`](./examples/streaming/chat) は名前付き Thread で 1 つのタスクをストリーミング実行します。
- [`threads`](./examples/threads)：継続、継続のみ、分岐、checkpoint の監査。
- [`structured-output`](./examples/structured-output)：型付き JSON 出力。
- [`tools`](./examples/tools)：MCP を自分で管理せず、型付き Go 関数を実際のローカル provider に公開します。
- [`skills`](./examples/skills)：skill を選択し、その証拠ファイルを検証します。[`profiles`](./examples/profiles) は profile リソースを同期し、必要に応じてローカルプローブやタスク送信を行います。
- [`inspect`](./examples/inspect)：2 回の実際の呼び出し後に、プローブと宣言済みの reviewer skill の選択を行います。
- [`web-chat`](./examples/web-chat)：SSE/AG-UI server と、[`aguiclient`](./examples/web-chat/aguiclient)、[`copilotkit`](./examples/web-chat/copilotkit) の 2 つのフロントエンド。
- [`a2a-server`](./examples/a2a-server)：A2A 経由で Agent を公開し、呼び出します。
- [`showcases/team-agent-workflow`](./examples/showcases/team-agent-workflow)：計画、実装、レビューを 1 つのパイプラインにまとめます。

実際に呼び出す例には、対応する CLI とログイン状態が必要です。リポジトリの通常のテストでは、有料の呼び出しは発生しません。

## 責務の境界

core ライブラリは、汎用の HTTP/gRPC server、ホストのタスクキュー、スケジューラー、マルチテナント、認可、データベースを提供せず、どの Agent にタスクを割り当てるかも決めません。プロトコルの公開は bridges とホストアプリケーションが、チームの役割とワークフロー方針はホストが担当します。

## ドキュメント

- [ドキュメントマップ](./docs/README.md)
- [API リファレンス](./docs/api-reference.md)
- [ホスト定義の Tools](./docs/tools.md)
- [ストリーミングガイド](./docs/streaming.md)
- [構造化出力](./docs/structured-output.md)
- [実行ポリシー：sandbox、承認、タイムアウト](./docs/run-policy.md)
- [A2A 連携](./docs/a2a.md)
- [公開エラー](./docs/public-errors.md)

## v1 から v2 への移行

SDK の import と依存関係には、すべて `/v2` のモジュールパスを使ってください。

| v1 | v2 |
|---|---|
| `agent.Run(ctx, prompt, opts...)` | `agent.Send(ctx, prompt, opts...).Result()` |
| `agent.Stream(ctx, prompt, opts...)` | `agent.Send(ctx, prompt, opts...)` |
| `thread.Run` / `thread.Stream` | `thread.Send` の後に `Result()` または `Events()` |
| 2 つの実行メソッドを持つ `Runner` | `Send` だけを持つ `Runner` |
| `RunFinished` を完了とみなす | 最終結果を確定する `Done` を読むか `Result()` を呼ぶ |
| Thread に明示的な store が必要 | 既定は Agent ごとのメモリ。再起動後の復旧には永続 store を指定 |

`RunAs[T]` は同期的な型付き helper として残り、内部で `Send` を使います。通常の進捗イベントを読む前に `Events()` を購読してください。Result だけを取得する呼び出しでも、重要なイベントと破棄の要約を後から読むために保持します。追加入力には `Stream.Send` を、継続的なチャットには `Thread.Standby(serverCtx)` を使います。終了時には、引き続き `Agent.Close(ctx)` で Agent が所有するリソースを解放します。

## ライセンス

別途記載がない限り、このリポジトリは [Apache License, Version 2.0](./LICENSE) に従って配布されます。第三者の資料には、それぞれのライセンスと著作権表示が引き続き適用されます。[第三者に関する通知](./THIRD_PARTY_NOTICES.md)を参照してください。

Codex、Claude、Cursor、CodeBuddy、およびその他の製品名は、それぞれの所有者の商標です。これらの名称は対応する連携先を示すためだけに使われており、このプロジェクトが権利者と提携している、または権利者の推奨を受けていることを意味しません。
