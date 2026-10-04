package sessionrecorder

import (
	"errors"
	"unicode/utf8"

	adaptor "github.com/agent-dance/agent-adaptor"
)

// Validate before consulting any backend, so memory and custom backends retain
// the same message contract as the durable JSON envelope.
func validateRecordedMessage(ev adaptor.Event) error {
	switch e := ev.(type) {
	case adaptor.ImageContent:
		return e.Validate()
	case *adaptor.ImageContent:
		if e == nil {
			return errors.New("sessionrecorder: cannot record a nil image")
		}
		return e.Validate()
	case adaptor.TextDelta:
		return validateRecordedUserID(e)
	case *adaptor.TextDelta:
		if e != nil {
			return validateRecordedUserID(*e)
		}
	}
	return nil
}

func validateRecordedUserID(e adaptor.TextDelta) error {
	if e.Role == adaptor.RoleUser && !utf8.ValidString(e.UserID) {
		return errors.New("sessionrecorder: user identity requires valid UTF-8")
	}
	return nil
}
