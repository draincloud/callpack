// Package integration exercises registry and the consul caller middleware against one
// another: what registry writes to the catalog is what the middleware has to be able to
// resolve. Neither module depends on the other, so the round trip has no other home.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/draincloud/callpack/caller"
	"github.com/draincloud/callpack/caller/middleware/consul"
	"github.com/draincloud/callpack/registry"
	"github.com/hashicorp/consul/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agent is a consul agent that serves the registration side registry writes to and the
// health side the middleware reads from.
type agent struct {
	mu       sync.Mutex
	services map[string]*api.AgentServiceRegistration
	checks   map[string]string
}

func newAgent(t *testing.T) *api.Client {
	t.Helper()

	a := &agent{
		services: map[string]*api.AgentServiceRegistration{},
		checks:   map[string]string{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/agent/service/register", a.register)
	mux.HandleFunc("PUT /v1/agent/service/deregister/{id}", a.deregister)
	mux.HandleFunc("PUT /v1/agent/check/update/{id}", a.update)
	mux.HandleFunc("GET /v1/health/service/{name}", a.health)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	config := api.DefaultConfig()
	config.Address = strings.TrimPrefix(srv.URL, "http://")
	client, err := api.NewClient(config)
	require.NoError(t, err)

	return client
}

func (a *agent) register(w http.ResponseWriter, r *http.Request) {
	var svc api.AgentServiceRegistration
	if err := json.NewDecoder(r.Body).Decode(&svc); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.services[svc.ID] = &svc
	a.checks[svc.Check.CheckID] = svc.Check.Status
}

func (a *agent) deregister(_ http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.services, r.PathValue("id"))
}

func (a *agent) update(w http.ResponseWriter, r *http.Request) {
	var update struct{ Status, Output string }
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.checks[r.PathValue("id")] = update.Status
}

func (a *agent) health(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	passingOnly := r.URL.Query().Get("passing") != ""

	a.mu.Lock()
	entries := []*api.ServiceEntry{}
	for _, svc := range a.services {
		if svc.Name != name || (passingOnly && a.checks[svc.Check.CheckID] != api.HealthPassing) {
			continue
		}
		entries = append(entries, &api.ServiceEntry{
			Node:    &api.Node{Node: "node", Address: "10.0.0.1"},
			Service: &api.AgentService{ID: svc.ID, Service: svc.Name, Address: svc.Address, Port: svc.Port},
		})
	}
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

func register(t *testing.T, client *api.Client, svc registry.Service) {
	t.Helper()

	if svc.Logger == nil {
		svc.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	reg, err := registry.Register(t.Context(), client, svc)
	require.NoErrorf(t, err, "register %q", svc.Name)
	t.Cleanup(func() { _ = reg.Close() })
}

func split(t *testing.T, rawURL string) (host string, port int) {
	t.Helper()

	host, rawPort, _ := strings.Cut(strings.TrimPrefix(rawURL, "http://"), ":")
	_, err := fmt.Sscanf(rawPort, "%d", &port)
	require.NoErrorf(t, err, "port of %q", rawURL)

	return host, port
}

func TestRegisteredServiceIsDiscoverableThroughTheMiddleware(t *testing.T) {
	client := newAgent(t)

	replicas := map[string]bool{}
	var mu sync.Mutex
	for _, name := range []string{"one", "two"} {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			replicas[name] = true
			mu.Unlock()
			fmt.Fprintf(w, "served by %s, host %s", name, r.Host)
		}))
		t.Cleanup(backend.Close)

		host, port := split(t, backend.URL)
		register(t, client, registry.Service{Name: "api", Address: host, Port: port})
	}

	call := caller.New(http.Client{}, consul.Resolve(client, 0))
	for i := range 2 {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://api/things", nil)
		require.NoError(t, err)

		resp, err := call.Do(req)
		require.NoErrorf(t, err, "request %d", i)

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		assert.Contains(t, string(body), "host api", "want the service name as the Host header")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, replicas, 2, "want both replicas load balanced across, reached %v", replicas)
}

func TestUnhealthyServiceIsNotDiscoverable(t *testing.T) {
	client := newAgent(t)

	register(t, client, registry.Service{
		Name: "api", Address: "10.1.2.3", Port: 8080, TTL: 40 * time.Millisecond,
		Health: func(context.Context) error { return fmt.Errorf("not ready") },
	})

	call := caller.New(http.Client{}, consul.Resolve(client, time.Millisecond))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://api/things", nil)
	require.NoError(t, err)

	_, err = call.Do(req)
	assert.Error(t, err, "the unhealthy replica was routed to")
}
