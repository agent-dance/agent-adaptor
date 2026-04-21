package appserver

import (
	"encoding/json"
	"io"
	"sync"
)

// stdioStream adapts a subprocess's stdin (io.WriteCloser) and stdout
// (io.Reader) into a single jsonrpc2.ObjectStream. JSON objects are read
// from stdout using a streaming decoder and written to stdin using a
// streaming encoder.
//
// This replaces the previous jrpc2/channel.Line + tolerantChannel
// combination used by agent-adaptor up to mid-2026. sourcegraph/jsonrpc2
// does not require the mandatory "jsonrpc":"2.0" marker on inbound
// frames (its Request/Response UnmarshalJSON implementations simply
// delete the field into ExtraFields / ignore it as an unknown field), so
// the whole tolerant-marker machinery is no longer needed.
//
// Order guarantees: the underlying sourcegraph/jsonrpc2 Conn dispatches
// notifications synchronously to Handler.Handle — each one must return
// before the next is read. This preserves wire order all the way to
// our user-facing handler. Compare with the previous creachadair/jrpc2
// client which spawned a goroutine per message and reordered under mutex
// contention.
type stdioStream struct {
	stdin   io.WriteCloser
	decoder *json.Decoder
	enc     *json.Encoder
	wmu     sync.Mutex
}

// newStdioStream returns an ObjectStream bound to a subprocess's I/O.
func newStdioStream(stdin io.WriteCloser, stdout io.Reader) *stdioStream {
	return &stdioStream{
		stdin:   stdin,
		decoder: json.NewDecoder(stdout),
		enc:     json.NewEncoder(stdin),
	}
}

// ReadObject decodes the next JSON object from stdout into v.
func (s *stdioStream) ReadObject(v interface{}) error {
	return s.decoder.Decode(v)
}

// WriteObject marshals v and writes it (plus a trailing newline) to
// stdin. It is guarded by a mutex because jsonrpc2.Conn may invoke
// concurrent writes when a Call and a Notify race.
func (s *stdioStream) WriteObject(v interface{}) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.enc.Encode(v)
}

// Close closes the subprocess's stdin. The peer reads EOF and shuts
// down; the caller is expected to wait on the process afterwards.
func (s *stdioStream) Close() error {
	return s.stdin.Close()
}
