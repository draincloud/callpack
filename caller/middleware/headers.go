package middleware

import (
	"net/http"
	"strings"
)

// Header sets the header on every request going through the client.
func Header(key, value string) RoundTripperHandler {
	return headerHandler(key, value, false)
}

func headerHandler(key, value string, secret bool) func(http.RoundTripper) http.RoundTripper {
	return func(next http.RoundTripper) http.RoundTripper {
		fn := func(req *http.Request) (*http.Response, error) {
			if secret && !onOriginalHost(req) {
				// the client copies headers it doesn't recognise as credentials from the original request to every
				// hop, so the caller's own value of such a key goes as well. For the recognised ones it copies
				// nothing off the original host and what is there belongs to the destination, left alone
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

// roundTrip passes the request down the chain and fills the request in on the response if the transport below left
// it unset, keeping the chain onOriginalHost walks complete for any custom transport
func roundTrip(next http.RoundTripper, req *http.Request) (*http.Response, error) {
	resp, err := next.RoundTrip(req)
	if resp != nil && resp.Request == nil {
		resp.Request = req
	}
	return resp, err //nolint:wrapcheck // the transport's error goes through the middleware as it is
}

// credentialHeader reports if the header carries credentials, the set matching the one the standard client
// strips on a redirect to another host
func credentialHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "Authorization", "Www-Authenticate", "Cookie", "Cookie2", "Proxy-Authorization", "Proxy-Authenticate":
		return true
	}
	return false
}

// onOriginalHost reports if the request is still on the host the redirect chain started from, or on one of its
// subdomains. A request outside of a redirect chain is always on its own host. Once the chain left the original
// host the result stays negative for the rest of it, matching the standard client.
//
// The chain is walked over Response.Request, which the middleware fills in for the transport below it. A redirect
// the origin still can't be established for is treated as a hop away from it, and so is a hop between the unicode
// and the punycode form of the same internationalised host, which is compared as it is written.
func onOriginalHost(req *http.Request) bool {
	if req.Response == nil { // not a redirect
		return true
	}

	origin := req
	for origin.Response != nil {
		if origin.Response.Request == nil { // broken chain, the origin is unknown
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

// domainOrSubdomain reports whether sub is the same domain as parent or a subdomain of it
func domainOrSubdomain(sub, parent string) bool {
	if sub == parent {
		return true
	}
	if strings.ContainsAny(sub, ":%") { // IPv6 address or a zone, never a hostname
		return false
	}
	if !strings.HasSuffix(sub, parent) {
		return false
	}
	return sub[len(sub)-len(parent)-1] == '.'
}
