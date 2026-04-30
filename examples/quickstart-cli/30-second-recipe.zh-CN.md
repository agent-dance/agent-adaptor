# 30-second-recipe · quickstart-cli

[English Version](./30-second-recipe.md)

`agent-adaptor` 最小可能的集成：一条 prompt 进，一段 assistant 文本出。把它复制进一个全新模块，你就可以发版了。

## 1. Install

```bash
go get github.com/agent-dance/agent-adaptor@latest && go mod tidy
```

## 2. `main.go`（12 行）

```go
package main

import (
	"context"
	"fmt"
	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
)

func main() {
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{Model: "gpt-5.4"})))
	r, _ := sdk.Run(context.Background(), "Reply with a short acknowledgement.")
	fmt.Println(r.Output)
}
```

把 `codex` 换成 `claude` 或 `cursor`，程序的其余部分一字不变 —— 这就是 §2.2 "default agent binding" 在屏幕上兑现的承诺。

## 3. Run

```bash
go run .
```

## 4. 成功长什么样（真实运行，codex@gpt-5.4）

```
The quickstart example looks clear and sufficient as a baseline. It shows the intended SDK path without introducing extra concepts too early.
```

那一行就是 `result.Output`。完整的四联屏拆解（Output / Summary / RawStreams / Transcript）见 [`walkthrough.md`](./walkthrough.md)。
