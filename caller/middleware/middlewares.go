package middleware

import "net/http"

// RoundTripperHandler is a type for middleware handler
type RoundTripperHandler func(http.RoundTripper) http.RoundTripper

// RoundTripperFunc is a functional adapter for RoundTripperHandler
type RoundTripperFunc func(*http.Request) (*http.Response, error)

// RoundTrip adopts function to the type
func (rt RoundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return rt(r) }
