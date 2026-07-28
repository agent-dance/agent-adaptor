package a2a

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Auth attaches client-owned credentials to discovery and protocol requests.
type Auth interface {
	Wrap(base http.RoundTripper) http.RoundTripper
	Headers() map[string]string
}

type bearerToken string

// BearerToken returns authentication that sends token as an HTTP bearer
// credential to trusted A2A origins. An empty token sends no credential.
func BearerToken(token string) Auth {
	return bearerToken(token)
}

// BearerTokenFromEnv reads name immediately and returns bearer authentication
// for the resulting value. An unset or empty variable sends no credential.
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

type trustedOriginSet map[string]struct{}

func newTrustedOriginSet(agentCardURL string, extra []string) (trustedOriginSet, error) {
	baseOrigin, err := canonicalOrigin(agentCardURL)
	if err != nil {
		return nil, err
	}
	out := trustedOriginSet{baseOrigin: {}}
	for _, raw := range extra {
		origin, err := canonicalOrigin(raw)
		if err != nil {
			return nil, err
		}
		out[origin] = struct{}{}
	}
	return out, nil
}

func (s trustedOriginSet) AllowsURL(u *url.URL) bool {
	if len(s) == 0 || u == nil {
		return false
	}
	origin, err := canonicalOrigin(u.String())
	if err != nil {
		return false
	}
	_, ok := s[origin]
	return ok
}

func (s trustedOriginSet) Allows(raw string) bool {
	origin, err := canonicalOrigin(raw)
	if err != nil {
		return false
	}
	_, ok := s[origin]
	return ok
}

func canonicalOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("origin must be absolute")
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", errors.New("origin host is required")
	}
	port := u.Port()
	if port == "" {
		port = defaultPort(scheme)
	}
	if port != "" {
		return scheme + "://" + net.JoinHostPort(host, port), nil
	}
	return scheme + "://" + host, nil
}

func defaultPort(scheme string) string {
	switch scheme {
	case "http":
		return strconv.Itoa(80)
	case "https":
		return strconv.Itoa(443)
	default:
		return ""
	}
}

func cloneHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	copy := *base
	return &copy
}

func httpClientWithAuth(base *http.Client, auth Auth, trusted trustedOriginSet) *http.Client {
	copy := cloneHTTPClient(base)
	transport := copy.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	transport = stripAuthOnUntrustedTransport(transport, trusted)
	if auth != nil {
		transport = auth.Wrap(transport)
	}
	copy.Transport = transport
	copy.CheckRedirect = secureRedirectPolicy(copy.CheckRedirect, trusted)
	return copy
}

func secureRedirectPolicy(next func(*http.Request, []*http.Request) error, trusted trustedOriginSet) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if !trusted.AllowsURL(req.URL) {
			req.Header.Del("Authorization")
			req.Header.Del("Proxy-Authorization")
		}
		if next != nil {
			return next(req, via)
		}
		return nil
	}
}

func stripAuthOnUntrustedTransport(base http.RoundTripper, trusted trustedOriginSet) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if trusted.AllowsURL(req.URL) {
			return base.RoundTrip(req)
		}
		if req.Header.Get("Authorization") == "" && req.Header.Get("Proxy-Authorization") == "" {
			return base.RoundTrip(req)
		}
		req = req.Clone(req.Context())
		req.Header = req.Header.Clone()
		req.Header.Del("Authorization")
		req.Header.Del("Proxy-Authorization")
		return base.RoundTrip(req)
	})
}
