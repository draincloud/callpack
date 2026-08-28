package consul

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/draincloud/callpack/caller/middleware"
	"github.com/hashicorp/consul/api"
)

const DefaultTTL = time.Second

func Resolve(client *api.Client, ttl time.Duration) middleware.RoundTripperHandler {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	c := &cache{health: client.Health(), ttl: ttl, services: map[string]*service{}}

	return func(next http.RoundTripper) http.RoundTripper {
		return middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			addr, err := c.address(req.Context(), req.URL.Hostname())
			if err != nil {
				return nil, err
			}

			out := req.Clone(req.Context())
			if out.Host == "" {
				out.Host = req.URL.Host
			}
			out.URL.Host = addr

			return next.RoundTrip(out)
		})
	}
}

type cache struct {
	health *api.Health
	ttl    time.Duration

	mu       sync.Mutex
	services map[string]*service
}

type service struct {
	mu        sync.Mutex
	addresses []string
	next      uint64
	expires   time.Time
}

func (c *cache) address(ctx context.Context, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("consul: request has no host to resolve")
	}

	c.mu.Lock()
	s, ok := c.services[name]
	if !ok {
		s = &service{}
		c.services[name] = s
	}
	c.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	if time.Now().After(s.expires) {
		addresses, err := c.lookup(ctx, name)
		if err != nil {
			return "", err
		}
		s.addresses, s.expires, s.next = addresses, time.Now().Add(c.ttl), 0
	}

	address := s.addresses[s.next%uint64(len(s.addresses))]
	s.next++
	return address, nil
}

func (c *cache) lookup(ctx context.Context, name string) ([]string, error) {
	entries, _, err := c.health.Service(name, "", true, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("consul: failed to resolve service %q: %w", name, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("consul: service %q has no healthy instances", name)
	}

	addresses := make([]string, 0, len(entries))
	for _, entry := range entries {
		host := entry.Service.Address
		if host == "" {
			host = entry.Node.Address
		}
		addresses = append(addresses, net.JoinHostPort(host, strconv.Itoa(entry.Service.Port)))
	}
	return addresses, nil
}
