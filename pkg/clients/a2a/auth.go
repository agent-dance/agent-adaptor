package a2a

import (
	"net/http"
	"os"
)

// Auth attaches client-owned credentials to discovery and protocol requests.
type Auth interface {
	Wrap(base http.RoundTripper) http.RoundTripper
	Headers() map[string]string
}

type bearerToken string

func BearerToken(token string) Auth {
	return bearerToken(token)
}

func BearerTokenFromEnv(name string) Auth {
	return bearerToken(os.Getenv(name))
}

func (b bearerToken) Wrap(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if b != "" {
			req = req.Clone(req.Context())
			req.Header.Set("Authorization", "Bearer "+string(b))
		}
		return base.RoundTrip(req)
	})
}

func (b bearerToken) Headers() map[string]string {
	if b == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + string(b)}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
