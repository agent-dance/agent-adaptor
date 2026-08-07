# agent-adaptor

[English](./README.md) | [简体中文](./README.zh-CN.md) | [한국어](./README.ko.md) | [Deutsch](./README.de.md)

`agent-adaptor` は SDK であり、シンプルで直感的な API を通じて `Codex`、`Claude Code`、`Cursor`、`CodeBuddy` といった異なる形態の Agent を統一的に駆動し、基本的な呼び出しを超える多くの拡張機能を提供する。

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.6-sol"}))
result, err := agent.Run(ctx, "失敗しているテストを修正して")
```

Claude Code に切り替えるには構築時の Driver を差し替えるだけでよく、その他のコードは変更不要。

## 機能概要

- **統一設定**：1 セットの API で、異なる Agent の skills / MCP / システムプロンプト / モデル / sandbox / ツール / 承認を制御する。
- **ストリーミング応答**：任意でストリーミング出力を利用でき、シーンに応じて思考過程、テキスト出力、ツール呼び出し、意思決定リクエストを識別する。
- **会話管理**：会話のシームレスな継続と分岐に対応。業務側の ID（チケット番号やユーザー ID など）をそのまま会話識別子として使え、低レイヤの複雑な会話管理を意識する必要がない。
- **人による意思決定**：コールバックまたはイベントを通じて、質問への回答、高リスクコマンドの遮断、計画の確認が手軽に行える。意思決定の書き戻し機構を内蔵しており、ローカルに限らずクラウドへ意思決定を永続化できる。

## 高度な機能

- **構造化出力**：Go の構造体を定義して `RunAs[T]` を呼ぶだけで、Agent を実行しつつデータの埋まったオブジェクトを返すよう制約できる。
- **マルチプロトコル修飾**：A2A / AGUI などのプロトコル修飾を内蔵しており、1 行で Agent を SSE + AGUI ストリーミング出力対応の標準 Agent へラップできる。業務側のカスタムフロントエンドやクライアントと組み合わせれば、成熟した Agent サービスを提供できる（実行可能な CopilotKit フロントエンドのサンプル付き）。
- **Multi Agent**：Driver をまたぐ Team Agent モードに対応。たとえば Codex を Leader Agent とし、Plan Agent（Codex）、Coding Agent（Claude）、Reviewer Agent（Cursor）を自律的に制御して協調作業させることができ、すべての進捗と出力は Leader Agent のイベントストリームへ自動的に集約される（examples/showcases/team-agent-workflow のサンプルを参照）。
- **Agent の分離**：ローカルの Agent 設定とログイン状態を独立したディレクトリへ複製して実行でき、変更がローカルで使用中の Agent に影響しない。そのため、複数の Codex / Claude Code インスタンスを同時に作って並行開発したり、異なるロールを演じさせたりすることが容易にできる。

## インストール

```bash
go get github.com/agent-dance/agent-adaptor
```

Go 1.26.5 以上が必要。

重要：**実行時には対応する Agent がインストール済みかつログイン済みである必要がある**

## クイックスタート

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

	result, err := agent.Run(context.Background(), "失敗しているテストを修正して")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}
```

4 つの組み込み Driver は構築方法が同じで、それぞれ独自の `Config` を持つ：

```go
codexAgent := adaptor.New(codex.Driver(codex.Config{}))
claudeAgent := adaptor.New(claude.Driver(claude.Config{}))
cursorAgent := adaptor.New(cursor.Driver(cursor.Config{}))
codeBuddyAgent := adaptor.New(codebuddy.Driver(codebuddy.Config{}))
```

## ストリーミング実行

`Stream` は 1 回の実行を 1 本の型付きイベントストリームに展開し、終了時に `Result` を返す：

```go
stream := agent.Stream(ctx, "コミット予定のパッチを説明して")
defer stream.Cancel()

for event := range stream.Events() {
	switch event := event.(type) {
	case adaptor.TextDelta:
		fmt.Print(event.Text)
	case adaptor.Thinking:
		fmt.Fprint(os.Stderr, event.Text)
	case adaptor.ToolCall:
		if event.Phase == adaptor.PhaseStart {
			fmt.Printf("\n[ツール呼び出し：%s]\n", event.Name)
		}
	case *adaptor.ApprovalRequest:
		_ = event.Approve(ctx)
	case adaptor.Dropped:
		log.Printf("バックプレッシャーにより増分イベントを %d 件破棄した", event.Count)
	}
}

result, err := stream.Result()
```

テキスト、思考、ツール呼び出しとその結果、プロセス情報、ライフサイクル、サブ Agent の進捗、承認リクエストはすべてこの 1 本のストリームに乗り、第 2 のチャネルは存在しない。

途中で消費を打ち切る場合は `Cancel()` を呼ぶ。これは冪等である。

## 人による承認と sandbox

sandbox の強度、ネットワークとブラウザツール、承認モードは同じ `Policy` にまとまっており、構築時の指定が既定値、`Run` / `Stream` 側では呼び出しごとに丸ごと上書きできる：

```go
reviewer := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Sandbox:   adaptor.ReadOnly,    // 読み取り専用の workspace。レビューや計画系のロールに適する
		WebSearch: adaptor.FeatureDeny, // Web 検索を明示的に無効化
		Browser:   adaptor.FeatureDeny,
		Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAsk, // 高リスクなコマンドは人に委ねる
			PlanReview: adaptor.ApprovalAsk,
			Question:   adaptor.QuestionAsk, // 既定では質問は自動的に拒否される
			Timeout:    2 * time.Minute,
			OnTimeout:  adaptor.FallbackAbort,
		},
	}),
)
```

sandbox には `ReadOnly`、`WorkspaceWrite`、`Unrestricted` の 3 段階があり、`PolicyReadOnly` のようなプリセットは `Sandbox` だけを設定したショートカットにすぎない。選択した Driver がある次元をサポートしない場合は、プロセスを起動する前に明示的にエラーとなり、黙ってダウングレードすることはない。

承認の消費形態は 2 つあり、どちらか一方を選ぶ。コールバックを登録すればコールバック方式となり、CLI や無人運用に適する：

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		switch req.Kind {
		case adaptor.ApprovalPermission:
			return req.Approve(ctx)
		case adaptor.ApprovalQuestion:
			return req.Answer(ctx, "PostgreSQL を使う")
		default:
			return req.Deny(ctx, "計画には人による確認が必要")
		}
	}),
)
```

無人運用では、そのまま使える `adaptor.ApproveAll()` と `adaptor.DenyAll(reason)` も利用できる。

コールバックを登録しなければイベント方式となる。リクエストは `*adaptor.ApprovalRequest` としてイベントストリームに現れ、responder を自身に持つため、いったん保留しておき、後から任意の goroutine や別の HTTP リクエストで応答を書き戻せる——これはまさに Web シーンで必要となる形態である：

```go
for event := range stream.Events() {
	switch event := event.(type) {
	case *adaptor.ApprovalRequest:
		pending.Add(threadKey, event) // リクエストを保留し、フロントエンドへ渡して描画させる
	case adaptor.Notice:
		// SDK は確定したすべての意思決定をブロードキャストし、ポリシーによる自動承認や
		// タイムアウト時のフォールバックも含むため、保留リストをホスト側で突き合わせる必要はない。
		if event.Kind == adaptor.NoticeApprovalResolved {
			if id, ok := event.Data["request_id"].(string); ok {
				pending.Remove(threadKey, id)
			}
		}
	}
}
```

`pending` はホスト自身のストレージであり、フロントエンドはリクエストを受け取った後、別の HTTP リクエストで意思決定を書き戻す：

```go
func (h *host) resolveDecision(w http.ResponseWriter, r *http.Request) {
	req := h.pending.Take(threadKey, requestID)
	if err := req.Approve(r.Context()); err != nil {
		sse.WriteApprovalError(w, err) // 確定済み／期限切れ → 410、Kind 不一致 → 400
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

応答は exactly-once である：重複応答、Kind の不一致、実行が既に終了しているケースはいずれも安定したエラー（`ErrApprovalResolved`、`ErrApprovalKindMismatch`、`ErrApprovalExpired`）を返し、ゼロ値のリクエストも永久にブロックしない。誰も応答しない場合は `Policy.Approvals` の `OnTimeout` でフォールバックし、拒否された場合は `OnReject` に従う。保留したリクエストをどこへ置くかはホストが決めることであり、プロセス内メモリに限定されない。

完全に動作する Web HITL の経路は [`web-chat/copilotkit`](./examples/web-chat/copilotkit) を参照。`/decision/pending` と `/decision/resolve` の 2 つのエンドポイントがあり、ページを更新しても未決の意思決定を復元できる。

## マルチターン会話

Agent は既定でステートレス。会話の連続性が必要な場合は store を注入すればよい：

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithThreadStore(memory.NewStore()),
)
defer agent.Close(context.Background())

thread := agent.Thread("tenant-42/issue-123")        // 対応する会話が既にあれば継続し、なければ作成する
result, err := thread.Run(ctx, "この問題の調査を続けて")

only := agent.Thread("tenant-42/issue-123", adaptor.ResumeOnly()) // 継続のみで作成はしない
branch := thread.Fork("tenant-42/issue-123/plan-b")               // 現在の進捗から分岐する
```

いくつかの取り決め：

- **会話 key は業務側自身の文字列**であり、SDK はそのまま保存し、そのまま比較する。まったく新しい会話を始めるなら key を変える。SDK は古い key を新しい会話へ再バインドする入口を提供しない。
- **同一の Thread では同時に 1 回の実行のみ**。これはリースによって保証され、期限切れの実行が新しい状態を上書きすることはない。
- **継続の前に互換性を検証する**。Driver、モデル、解決後の実際の workspace、設定、skills、MCP がいずれも fingerprint の計算に参加し、そのうち 1 つでも変化していれば会話を誤って再利用することはない。
- **失敗は状態を汚染しない**。非ゼロ終了、プロトコルエラー、キャンセルはいずれも有効な checkpoint を生成せず、それまでの健全な会話レコードはそのまま保たれる。
- **常駐プロセスは既定で再利用される**。Windows、macOS、Linux の各環境で、Claude、CodeBuddy、Codex は明示的な Thread のもとでターンをまたいで同一プロセスを再利用する。特定のターンあるいは毎ターン新しいプロセスが必要なら `adaptor.WithSpawn()` を追加する。Cursor とステートレスな呼び出しでは常にターンごとに新しいプロセスを起動する。`Close` 後の実行は `ErrAgentClosed` を返す。

単一プロセスのシーンでは `memory.NewStore()` を使い、永続化が必要なら `threadstore.Store` を実装する。

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

Schema は Go の型から生成され、各社ネイティブの schema 制約を優先して用いる。現在のチャネルやポリシーがサポートしない場合はプロンプトによる制約とローカル検証へ自動的にフォールバックし、その両方が使えない場合にのみ実行前に失敗する。戻り値には typed な値と、完全な監査用 `Result` の両方が含まれる。

詳細は [`structured-output` サンプル](./examples/structured-output)と[構造化出力ドキュメント](./docs/structured-output.md)を参照。

## オプションとリソース

オプションの語彙は 1 セットのみで、スコープはコンパイル時に型で区別される：

| 型 | 使える場所 |
|---|---|
| `Option` | `adaptor.New` でのみ使用可能 |
| `CallOption` | `Run` / `Stream` でのみ使用可能 |
| `SharedOption` | 双方で使用可能。呼び出し側が構築側を上書きする |

マージ規則は 1 つだけ：近い側が遠い側を上書きし、skills は追加され、その他はそれぞれの取り決めに従って置換またはマージされる。

同じ 1 セットのオプションが、各社 Agent の主要な設定面をカバーする：

| 制御したいもの | 使うもの |
|---|---|
| モデル | `WithModel` |
| システムプロンプト | `WithInstructions` |
| 作業ディレクトリ | `WithWorkspace`、分離されたワークツリーには `WithWorkspaceSpec` |
| skills | `WithSkills` と `skill.Dir` / `skill.FS` / `skill.Inline` / `skill.Key` / `skill.Require` |
| MCP | `WithMCP` と `mcp.Stdio` / `mcp.HTTP` / `mcp.SSE` |
| sandbox、ネットワーク、ブラウザツール、承認 | `WithPolicy`、対話式ならさらに `OnApproval` |
| 設定ディレクトリとリソース | `WithProfile`、`WithProfileResources` |
| タイムアウト、監査メタデータ、呼び出し元の identity | `WithTimeout`、`WithMetadata`、`WithIdentity` |
| 会話の永続化 | `WithThreadStore` |

```go
agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithModel("gpt-5.4"),
	adaptor.WithInstructions("あなたはこのリポジトリのレビュアーです：コードは読むだけにとどめ、先に結論、次に根拠を示してください。"),
	adaptor.WithSkills(skill.Dir("./skills/review")),
	adaptor.WithMCP(mcp.Stdio("repo-tools", "repo-mcp", mcp.Args("serve"))),
	adaptor.WithProfile(profile.Dedicated("./profiles/reviewer")),
	adaptor.WithTimeout(10*time.Minute),
)

result, err := agent.Run(ctx, "この変更をレビューして",
	adaptor.WithModel("gpt-5.4-mini"),
	adaptor.WithSkills(skill.Require(skill.Dir("./skills/security"), "今回はセキュリティチェックを必ず通すこと")), // 追加であり、既定の skills を置き換えない
	adaptor.WithMetadata("request_id", requestID),
)
```

同じ設定でも Driver を変えれば別の Agent になる。ある Driver がそのうちのある能力をサポートしない場合は、起動前に明示的にエラーとなり、こっそり無視されることはない。

```go
codexReviewer := adaptor.New(codex.Driver(codex.Config{}), reviewerOptions...)
claudeReviewer := adaptor.New(claude.Driver(claude.Config{}), reviewerOptions...)
```

## ホスト定義 Tools

typed な Go 関数で直接 Agent に能力を追加でき、MCP server を自分で構築・保守する必要はない：

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

`WithTools` は構築時にのみ使用でき、Tool の集合をまとめて置き換える。schema は既定で handler の Go の型から推論されるが、標準の JSON Schema を明示的に与えることもできる。`tool.Reject(code, message)` はモデルへ安全に提示できる業務的な失敗を表し、通常の error や panic はサニタイズされる。ステートフルな Thread で使う Tool にはそれぞれ `tool.Revision` を設定し、handler の挙動の変化が継続の互換性判定に入るようにする。

MCP はここでは内部的な配送機構にすぎない：既存またはリモートの MCP server は従来どおり `WithMCP` を通す。組み込み Driver は Tools を SDK 自身の分離された profile へ実体化し、あなたが設定したネイティブの profile には手を加えない。ライフサイクル、schema、エラー、セキュリティと Thread のセマンティクスは[ホスト定義 Tools の契約](./docs/tools.md)を参照。

## Agent の分離

`WithProfile` はその Agent がどの provider 設定ディレクトリを使うかを決める。`profile.CloneNative` はローカルのネイティブ設定から独立した profile を複製し、settings、MCP、skills を持ち込むかどうかを選択できる。ログイン状態は token をコピーするのではなくリンクで共有する：

```go
worker := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithProfile(profile.CloneNative("/var/agents/worker-1",
		profile.CopySettings(),
		profile.CopyMCP(),
		profile.CopySkills(),
		profile.LinkAuth(), // シンボリックリンクでローカルのログイン状態を共有し、ローカル側の変更に自動追従する
	)),
)
```

これにより、同じ CLI をロール単位やタスク単位で複数インスタンス並行して動かせるようになり、それぞれの設定変更は互いに影響せず、ローカルで使用中の `~/.claude`、`~/.codex` にも手を加えない：

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

ほかに 3 つの選択肢がある：`profile.Native()` はローカルのネイティブ設定をそのまま使う。`profile.Dedicated(dir)` は自分で管理するディレクトリに固定する。`profile.CloneFrom(src, dst, ...)` はテンプレートディレクトリから派生させる。profile は会話の fingerprint に参加するため、構築時のオプションにしかなり得ず、呼び出しごとに切り替えることはできない。

宣言したリソースが実際に何を実体化したのか、Driver が本当にそれを認識しているのかは、`agent.ProfileState(ctx)` で読み取り、`agent.SyncProfile(ctx)` で実体化する。いずれも実際に観測した結果のみを報告する。完全なデモは [`profiles` サンプル](./examples/profiles)を参照。

## Result とエラー

成功時は `*Result, nil` を返す。失敗は Go の `error` という 1 本の経路のみを通る：実行は完了したが業務的に失敗した場合は、利用可能な `Result` を伴う `*RunError` を返す。インフラ系の失敗は通常のラップ可能な error である。

```go
result, err := agent.Run(ctx, prompt)
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		log.Printf("実行に失敗：%s、取得済みの要約：%s", runErr.Reason, runErr.Result.Summary)
	}
	return err
}
```

`Result` の各層の出力は互いを汚染しない：

| フィールド | 内容 |
|---|---|
| `Text` | 最終的にユーザーへ向けた回答テキスト |
| `Summary` | 一覧、ログ、issue コメントに適した短い要約 |
| `Raw()` | 完全な stdout、stderr、および各社の正式プロトコルの終局 payload |
| `Transcript()` | Driver が正式プロトコルから解析した標準化済みの項目 |
| `Services()` | 今回の実行で実際に観測された runtime services |
| `Decode()` | 検証済みの構造化出力 |
| `Usage` / `Model` / `Provider` / `Metadata` | 使用量と監査情報 |

`Text` に生の stdout が混ざることはなく、要約や各社の終局 payload が自動的に連結されることもない。`Run` と `Stream.Result()` から得られる内容はフィールドごとに等価である。

## 上位アプリケーションとの統合

**Web フロントエンド**：1 行で Agent を `http.Handler` にラップし、AG-UI プロトコルで通信する。AG-UI 互換のクライアント（たとえば CopilotKit）はそのまま接続できる：

```go
mux.Handle("/agent", sse.Handler(agent, sse.Options{
	Protocol: sse.AGUI,
}))
```

**A2A**：`bridges/a2a` は任意の Runner を A2A server として公開する。ホストはルーティング、認証、TLS を担当するだけでよい：

```go
server := bridgea2a.NewServer(agent, bridgea2a.ServerOptions{
	AgentCard: bridgea2a.AgentCard{
		Name:        "Local coding agent",
		Description: "Runs coding tasks through agent-adaptor",
		Version:     "1.0.0",
		URL:         "https://host.example/a2a",
	},
	Session: bridgea2a.ThreadByContextID(), // リモートの contextID をローカルの Thread key へ安定的にマッピングする
	Options: []adaptor.CallOption{adaptor.WithPolicy(adaptor.PolicyWorkspaceWrite)},
})

mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
mux.Handle("/a2a", server.Handler())
```

逆にリモートの A2A Agent を呼び出すには `clients/a2a` を使う。返るのは A2A の task、message、artifact であり、リモートのプロトコルタスクがローカル CLI の stdout や `Result` を持つかのように装うことはない：

```go
client := clienta2a.New(clienta2a.Options{
	AgentCardURL: "https://remote.example/.well-known/agent-card.json",
	Auth:         clienta2a.BearerTokenFromEnv("REMOTE_A2A_TOKEN"),
})
defer client.Close()

task, err := client.Send(ctx, clienta2a.SendRequest{
	Message: clienta2a.Message{
		Role:  "user",
		Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "この変更をレビューして"}},
	},
})
```

途中経過が必要なら `SendStream` / `Subscribe` を使う。思考過程、ツール呼び出し、承認イベント、あるいは診断フィールドを外部へ露出するかどうかは `ExposurePolicy` が制御し、既定では最小限の露出となる。

## マルチ Agent 協調

`agent-adaptor` は A2A の標準プロトコルにより、Driver をまたぐマルチ Agent 協調を実現できる（したがって、任意のリモート A2A プロトコルの Agent もサポートする）。

Driver をまたぐ協調の価値は、モデルとそのネイティブな `Harness` との適合という利点を保てる点にある：GPT 系のモデルは Codex 上でより良く振る舞い、Claude 系のモデルは Claude Code 上でより高い能力を発揮する。そのため `agent-adaptor` の設計方針は、マルチモデル協調を実現するために、多くのモデルに対応するが性能の出ない汎用 Harness に妥協するのではなく、各モデルを最も適した Harness の中に留めたまま協調させることにある。

中核となるコード例は以下のとおり：

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

完全版の [`team-agent-workflow`](./examples/showcases/team-agent-workflow) には、ロール単位の sandbox、構造化された `PLAN.md` 成果物、workspace の監査、さらにリアルタイムのサブ Agent カードを備えた CopilotKit ページが含まれており、コマンド 1 つで起動できる：

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
```

## 環境プローブ

`Agent.Inspect()` は読み取り専用のプローブであり、起動前チェック、環境診断、モデル選択に用いる。サポートされないプローブは明確に unsupported を返し、データをでっち上げることはない：

```go
environment, err := agent.Inspect().Environment(ctx) // ヘルス状態と項目ごとの診断。そのまま描画できる
models, err := agent.Inspect().Models(ctx)
quota, err := agent.Inspect().Quota(ctx)
state, err := agent.ProfileState(ctx)                // 期待値と実測値を報告するのみで、変更はしない
synced, err := agent.SyncProfile(ctx)                // 設定リソースを明示的に実体化する
```

## 6 つの名詞

ライブラリ全体の公開モデルは 6 つの名詞だけで構成される：

| 名詞 | 意味 |
|---|---|
| `Agent` | 設定が揃っており、構築後すぐ実行できるエージェント |
| `Thread` | 業務 key で識別され、継続も分岐もできる一連の会話 |
| `Stream` | 進行中の 1 回の実行 |
| `Event` | 実行の過程で発生した 1 件の型付きイベント |
| `Result` | 1 回の実行の最終結果と監査情報 |
| `Driver` | ある Agent CLI の接続実装。拡張する側だけが気にすればよい |

付随する制約は、構築入口は 1 つ、オプションのマージ規則は 1 セット、実行パイプラインは 1 本、イベントストリームは 1 本、失敗判定の入口は 1 つ、というものである。

## パッケージ一覧

| パッケージ | 用途 |
|---|---|
| [`driver`](./driver) | Driver SPI。新しい Agent を接続するときに使う |
| [`codex`](./codex)、[`claude`](./claude)、[`cursor`](./cursor)、[`codebuddy`](./codebuddy) | 組み込み Driver とそれぞれの Config |
| [`tool`](./tool)、[`skill`](./skill)、[`mcp`](./mcp)、[`profile`](./profile) | 呼び出し側に向けた能力とリソースの語彙 |
| [`threadstore`](./threadstore)、[`memory`](./memory) | Thread 永続化のインタフェースとメモリ実装 |
| [`bridges`](./bridges) | SSE、AG-UI、A2A、subagent-stream のプロトコルブリッジ |
| [`clients/a2a`](./clients/a2a) | A2A クライアント |
| [`hosttools`](./hosttools) | 任意で使える委譲オーケストレーションとイベント記録のコンポーネント |
| [`adaptertest`](./adaptertest) | Driver 一貫性テストスイート |

自社の Agent CLI を接続するには：`driver.Driver` を実装し、`adaptertest` を通す。それ以降は組み込み Driver と同じ上位機能を得られる。

## サンプル

- [`quickstart`](./examples/quickstart)：Agent を構築して prompt を 1 回実行する。
- [`streaming`](./examples/streaming)：イベントの消費とキャンセル。
- [`threads`](./examples/threads)：継続、継続のみ、分岐、checkpoint の監査。
- [`structured-output`](./examples/structured-output)：typed な JSON 出力。
- [`tools`](./examples/tools)：実際のローカル provider へ typed な Go 関数を公開する。MCP を自分で管理する必要はない。
- [`skills`](./examples/skills) / [`profiles`](./examples/profiles)：skill の解決と実体化、設定リソースと同期。
- [`inspect`](./examples/inspect)：環境、モデル、クォータ、schema、skills と設定状態。
- [`web-chat`](./examples/web-chat)：SSE/AG-UI のサーバ。フロントエンドは [`aguiclient`](./examples/web-chat/aguiclient) と [`copilotkit`](./examples/web-chat/copilotkit) の 2 種類。
- [`a2a-server`](./examples/a2a-server)：A2A 経由で Agent を公開し、呼び出す。
- [`showcases/team-agent-workflow`](./examples/showcases/team-agent-workflow)：計画、実装、レビューを 1 本のパイプラインにつなぐ。

実際に呼び出しを行うサンプルは、対応する CLI とログイン状態に依存する。リポジトリの通常のテストで課金対象の呼び出しが発生することはない。

## 境界

コアライブラリは HTTP/gRPC server、キュー、スケジューラ、マルチテナント、認証、データベースを提供せず、あるタスクをどの Agent に割り当てるかを呼び出し側に代わって決めることもしない。プロトコルのサービス化は bridges と上位アプリケーションに委ね、チームのロールやフローのポリシーは業務側に委ねる。

## ドキュメント

- [ドキュメントマップ](./docs/README.md)
- [API reference](./docs/api-reference.md)
- [ホスト定義 Tools](./docs/tools.md)
- [ストリーミングガイド](./docs/streaming.md)
- [構造化出力](./docs/structured-output.md)
- [実行ポリシー：sandbox、承認、タイムアウト](./docs/run-policy.md)
- [A2A 統合](./docs/a2a.md)
- [公開エラー](./docs/public-errors.md)

## ライセンス

別途明記されている場合を除き、このリポジトリは
[Apache License, Version 2.0](./LICENSE) の下で提供されます。第三者の素材には
それぞれのライセンスと帰属表示が適用されます。詳しくは
[第三者に関する通知](./THIRD_PARTY_NOTICES.md) を参照してください。正式な条件は
`LICENSE` の英語本文に従います。

Codex、Claude、Cursor、CodeBuddy、およびその他の製品名は、それぞれの権利者の
商標です。これらの名称は対応する連携を示す目的でのみ使用しており、本プロジェクトは
各権利者との提携関係になく、各権利者による推奨を受けるものでもありません。
