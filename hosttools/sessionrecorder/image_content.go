package sessionrecorder

import (
	"errors"

	adaptor "github.com/agent-dance/agent-adaptor"
)

// Validate before consulting any backend, so memory and custom backends retain
// the same image contract as the durable JSON envelope.
func validateRecordedImage(ev adaptor.Event) error {
	switch e := ev.(type) {
	case adaptor.ImageContent:
		return e.Validate()
	case *adaptor.ImageContent:
		if e == nil {
			return errors.New("sessionrecorder: cannot record a nil image")
		}
		return e.Validate()
	default:
		return nil
	}
}
