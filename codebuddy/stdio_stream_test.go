package codebuddy

import (
	"bytes"
	"testing"
)

// bytesWriteCloser adapts a bytes.Buffer to io.WriteCloser so the stdio
// stream can be exercised without a real subprocess pipe.
type bytesWriteCloser struct {
	*bytes.Buffer
	closed bool
}

func (b *bytesWriteCloser) Close() error {
	b.closed = true
	return nil
}

// TestStdioStreamRoundtrip confirms the ACP stdio transport reads/writes
// newline-delimited JSON objects in FIFO order and closes the stdin half on
// Close. It mirrors the codex app-server smoke test since both share the same
// jsonrpc2 ObjectStream contract.
func TestStdioStreamRoundtrip(t *testing.T) {
	t.Parallel()

	stdin := &bytesWriteCloser{Buffer: &bytes.Buffer{}}
	stdout := bytes.NewBufferString(
		`{"id":1,"result":{"ok":true}}` + "\n" +
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1"}}` + "\n",
	)

	stream := newStdioStream(stdin, stdout)

	var first map[string]any
	if err := stream.ReadObject(&first); err != nil {
		t.Fatalf("read 1: %v", err)
	}
	if id, _ := first["id"].(float64); id != 1 {
		t.Fatalf("read 1 wrong object: %#v", first)
	}

	var second map[string]any
	if err := stream.ReadObject(&second); err != nil {
		t.Fatalf("read 2: %v", err)
	}
	if second["method"] != "session/update" {
		t.Fatalf("read 2 wrong object: %#v", second)
	}

	if err := stream.WriteObject(map[string]any{"hello": "world"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := stdin.String(); got != `{"hello":"world"}`+"\n" {
		t.Fatalf("unexpected stdin payload: %q", got)
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !stdin.closed {
		t.Fatalf("stdin not closed")
	}
}
