package middleware

import (
	"net/http"
	"strings"
)

// Header - adds headers to requests.
func Header(key, value string) RoundTripperHandler {
	return headerHandler(key, value, false)
}

// SecretHeader - adds a header carrying a credential to request.
func SecretHeader(key, value string) func(http.RoundTripper) http.RoundTripper {
	return headerHandler(key, value, true)
}

// JSON - sets Content-Type and Accept headers to json.
func JSON(next http.RoundTripper) http.RoundTripper {
	fn := func(req *http.Request) (*http.Response, error) {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		return next.RoundTrip(req)
	}
	return RoundTripperFunc(fn)
}

// BasicAuth - adds basic auth to request.
func BasicAuth(user, passwd string) func(http.RoundTripper) http.RoundTripper {
	return func(next http.RoundTripper) http.RoundTripper {
		fn := func(req *http.Request) (*http.Response, error) {
			if onOriginalHost(req) {
				req.SetBasicAuth(user, passwd)
			}
			return roundTrip(next, req)
		}
		return RoundTripperFunc(fn)
	}
}

func headerHandler(key, value string, secret bool) func(http.RoundTripper) http.RoundTripper {
	return func(next http.RoundTripper) http.RoundTripper {
		fn := func(req *http.Request) (*http.Response, error) {
			if secret && !onOriginalHost(req) {
				if !credentialHeader(key) {
					req.Header.Del(key)
				}
				return roundTrip(next, req)
			}
			req.Header.Set(key, value)
			return roundTrip(next, req)
		}
		return RoundTripperFunc(fn)
	}
}

func roundTrip(next http.RoundTripper, req *http.Request) (*http.Response, error) {
	resp, err := next.RoundTrip(req)
	if resp != nil && resp.Request == nil {
		resp.Request = req
	}
	return resp, err //nolint:wrapcheck
}

func credentialHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "Authorization", "Www-Authenticate", "Cookie", "Cookie2", "Proxy-Authorization", "Proxy-Authenticate":
		return true
	}
	return false
}

func onOriginalHost(req *http.Request) bool {
	if req.Response == nil {
		return true
	}

	origin := req
	for origin.Response != nil {
		if origin.Response.Request == nil {
			return false
		}
		origin = origin.Response.Request
	}

	originHost := strings.ToLower(origin.URL.Hostname())
	for r := req; r != origin; r = r.Response.Request {
		if !domainOrSubdomain(strings.ToLower(r.URL.Hostname()), originHost) {
			return false
		}
	}
	return true
}

func domainOrSubdomain(sub, parent string) bool {
	if sub == parent {
		return true
	}
	if strings.ContainsAny(sub, ":%") {
		return false
	}
	if !strings.HasSuffix(sub, parent) {
		return false
	}
	return sub[len(sub)-len(parent)-1] == '.'
}
