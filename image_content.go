package adaptor

import (
	"errors"
	"mime"
	"strings"
	"unicode/utf8"
)

// Validate checks the shape of a host image reference without accessing the
// resource. Type, MessageID and URL must be nonempty; MIMEType must name a
// concrete image media type. All image strings must be valid UTF-8. UserID is
// checked only for RoleUser, since assistant attribution is ignored. Only
// assistant and user roles are supported. Validation does not establish
// resource safety or user authenticity.
func (e ImageContent) Validate() error {
	for _, value := range []string{e.MessageID, e.Type, e.MIMEType, e.URL, e.Filename} {
		if !utf8.ValidString(value) {
			return errors.New("adaptor: image content requires valid UTF-8")
		}
	}
	if e.Role == RoleUser && !utf8.ValidString(e.UserID) {
		return errors.New("adaptor: image user identity requires valid UTF-8")
	}
	if e.MessageID == "" {
		return errors.New("adaptor: image content requires a message ID")
	}
	if e.Type == "" {
		return errors.New("adaptor: image content requires a UTF-8 type")
	}
	if e.URL == "" {
		return errors.New("adaptor: image content requires a URL")
	}
	mediaType, _, err := mime.ParseMediaType(e.MIMEType)
	if err != nil || !strings.HasPrefix(mediaType, "image/") || strings.Contains(mediaType, "*") {
		return errors.New("adaptor: image content requires an image MIME type")
	}
	if e.Role != RoleAssistant && e.Role != RoleUser {
		return errors.New("adaptor: image content has an unsupported role")
	}
	return nil
}
