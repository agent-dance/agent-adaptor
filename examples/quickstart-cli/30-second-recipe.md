# 30-second-recipe · quickstart-cli

The smallest possible integration of `agent-adaptor`: one prompt in, one assistant text out. Copy this into a fresh module and you're shipping.

## 1. Install

```bash
go get github.com/agent-dance/agent-adaptor@latest && go mod tidy
```

## 2. `main.go` (12 lines)

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

Swap `codex` for `claude` or `cursor` and the rest of the program is identical — that's the §2.2 "default agent binding" promise on screen.

## 3. Run

```bash
go run .
```

## 4. What success looks like (real run, codex@gpt-5.4)

```
The quickstart example looks clear and sufficient as a baseline. It shows the intended SDK path without introducing extra concepts too early.
```

That single line is `result.Output`. For the full four-panel breakdown (Output / Summary / RawStreams / Transcript), see [`walkthrough.md`](./walkthrough.md).
