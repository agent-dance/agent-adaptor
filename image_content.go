package adaptor

import (
	"errors"
	"mime"
	"strings"
	"unicode/utf8"
)

// Validate checks the shape of a host image reference without accessing the
// resource. Type must be a nonempty UTF-8 host discriminator, MessageID and URL must
// be nonempty, and
// MIMEType must name a concrete image media type. Only assistant and user roles
// are supported. This does not establish resource safety or user authenticity.
func (e ImageContent) Validate() error {
	if e.MessageID == "" {
		return errors.New("adaptor: image content requires a message ID")
	}
	if e.Type == "" || !utf8.ValidString(e.Type) {
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
