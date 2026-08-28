package caller

import (
	"fmt"
	"net/http"

	"github.com/draincloud/callpack/caller/middleware"
)

type Caller struct {
	client http.Client
	mws    []middleware.RoundTripperHandler
}

func New(client http.Client, mws ...middleware.RoundTripperHandler) *Caller {
	return &Caller{
		client: client,
		mws:    mws,
	}
}

func (r *Caller) Use(middlewares ...middleware.RoundTripperHandler) {
	r.mws = append(r.mws, middlewares...)
}

func (r *Caller) With(middlewares ...middleware.RoundTripperHandler) *Caller {
	combined := make([]middleware.RoundTripperHandler, 0, len(r.mws)+len(middlewares))
	combined = append(combined, r.mws...)
	combined = append(combined, middlewares...)
	return &Caller{client: r.client, mws: combined}
}

func (r *Caller) Client() *http.Client {
	cl := r.client
	if cl.Transport == nil {
		cl.Transport = http.DefaultTransport
	}
	for _, handler := range r.mws {
		cl.Transport = handler(cl.Transport)
	}
	return &cl
}

func (r *Caller) Do(req *http.Request) (*http.Response, error) {
	resp, err := r.Client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("caller: failed to execute request: %w", err)
	}
	return resp, nil
}
