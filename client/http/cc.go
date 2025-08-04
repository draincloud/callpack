package client

import (
	"net/http"

	"github.com/draincloud/callpack/client/gateway"
)

type Request = *http.Request
type Response = *http.Response

// Client - common http client interface.
type Client interface {
	Do(req Request) (Response, error)
}

type client struct {
	target  string
	gateway gateway.Gateway
	cc      *http.Client
}

func (c *client) Do(req Request) (Response, error) {
	resp, err := c.gateway.GetInstances(c.target, nil, nil)
	if err != nil {
		return nil, err
	}

	return c.cc.Do(req)
}
