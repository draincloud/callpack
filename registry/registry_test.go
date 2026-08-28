package registry_test

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
)

type agent struct {
	mu        sync.Mutex
	services  map[string]*api.AgentServiceRegistration
	checks    map[string]string
	forgotten bool
	registers int
	updates   int
}

func newAgent(t *testing.T) (*agent, *api.Client) {
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
	if err != nil {
		t.Fatalf("consul client: %v", err)
	}
	return a, client
}

func (a *agent) register(w http.ResponseWriter, r *http.Request) {
	var svc api.AgentServiceRegistration
	if err := json.NewDecoder(r.Body).Decode(&svc); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.registers++
	a.forgotten = false
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

	id := r.PathValue("id")
	if _, ok := a.checks[id]; !ok || a.forgotten {
		// what a real agent answers for a check it does not know about
		http.Error(w, fmt.Sprintf("CheckID %q does not have associated TTL", id), http.StatusInternalServerError)
		return
	}
	a.updates++
	a.checks[id] = update.Status
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

func (a *agent) service(id string) *api.AgentServiceRegistration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.services[id]
}

func (a *agent) status(checkID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.checks[checkID]
}

func (a *agent) counts() (registers, updates int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.registers, a.updates
}

func (a *agent) forget() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.forgotten = true
}

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func register(t *testing.T, client *api.Client, svc registry.Service) *registry.Registration {
	t.Helper()

	if svc.Logger == nil {
		svc.Logger = quiet()
	}
	reg, err := registry.Register(t.Context(), client, svc)
	if err != nil {
		t.Fatalf("register %q: %v", svc.Name, err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRegisterAdvertisesInstanceWithATTLCheck(t *testing.T) {
	a, client := newAgent(t)

	reg := register(t, client, registry.Service{
		Name:    "api",
		Address: "10.1.2.3",
		Port:    8080,
		Tags:    []string{"v2"},
		Meta:    map[string]string{"commit": "abc"},
	})

	svc := a.service(reg.ID())
	if svc == nil {
		t.Fatalf("service %q was not registered, agent has %v", reg.ID(), a.services)
	}
	if svc.Name != "api" || svc.Address != "10.1.2.3" || svc.Port != 8080 {
		t.Errorf("registered %q %s:%d, want api 10.1.2.3:8080", svc.Name, svc.Address, svc.Port)
	}
	if len(svc.Tags) != 1 || svc.Tags[0] != "v2" || svc.Meta["commit"] != "abc" {
		t.Errorf("tags %v and meta %v were not passed through", svc.Tags, svc.Meta)
	}
	if svc.Check.TTL != registry.DefaultTTL.String() {
		t.Errorf("check TTL is %q, want the default %q", svc.Check.TTL, registry.DefaultTTL)
	}
	if svc.Check.DeregisterCriticalServiceAfter == "" {
		t.Error("a killed replica would linger: DeregisterCriticalServiceAfter is unset")
	}
	if svc.Check.Status != api.HealthPassing {
		t.Errorf("check registered as %q, want %q so the instance is discoverable at once", svc.Check.Status, api.HealthPassing)
	}
}

func TestRegisterDetectsTheLocalAddress(t *testing.T) {
	a, client := newAgent(t)

	reg := register(t, client, registry.Service{Name: "api", Port: 8080})

	svc := a.service(reg.ID())
	if svc.Address == "" {
		t.Fatal("no address was detected, callers would fall back to the agent's node")
	}
	if strings.HasPrefix(svc.Address, "127.") {
		t.Errorf("detected %q, which no other host can reach", svc.Address)
	}
}

func TestRegisterRejectsAnIncompleteService(t *testing.T) {
	_, client := newAgent(t)

	for _, svc := range []registry.Service{{Port: 8080}, {Name: "api"}} {
		if _, err := registry.Register(t.Context(), client, svc); err == nil {
			t.Errorf("registering %+v succeeded, want an error", svc)
		}
	}
}

func TestReplicasOfAServiceRegisterSeparately(t *testing.T) {
	a, client := newAgent(t)

	one := register(t, client, registry.Service{Name: "api", Address: "10.1.2.3", Port: 8080})
	two := register(t, client, registry.Service{Name: "api", Address: "10.1.2.4", Port: 8080})

	if one.ID() == two.ID() {
		t.Fatalf("both replicas registered as %q, so one replaced the other", one.ID())
	}
	if a.service(one.ID()) == nil || a.service(two.ID()) == nil {
		t.Fatalf("only one replica is in the catalog: %v", a.services)
	}

	// a replica that restarts on the same address must replace itself, not pile up
	restarted := register(t, client, registry.Service{Name: "api", Address: "10.1.2.3", Port: 8080})
	if restarted.ID() != one.ID() {
		t.Errorf("restarted replica registered as %q, want %q", restarted.ID(), one.ID())
	}
}

func TestHeartbeatKeepsTheCheckPassing(t *testing.T) {
	a, client := newAgent(t)

	reg := register(t, client, registry.Service{
		Name: "api", Address: "10.1.2.3", Port: 8080, TTL: 40 * time.Millisecond,
	})

	waitFor(t, "heartbeats", func() bool {
		_, updates := a.counts()
		return updates >= 3
	})
	if got := a.status(reg.ID() + "-heartbeat"); got != api.HealthPassing {
		t.Errorf("check is %q after heartbeating, want %q", got, api.HealthPassing)
	}
}

func TestHeartbeatReportsAnUnhealthyInstance(t *testing.T) {
	a, client := newAgent(t)

	var failing bool
	var mu sync.Mutex
	health := func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if failing {
			return fmt.Errorf("database is unreachable")
		}
		return nil
	}

	reg := register(t, client, registry.Service{
		Name: "api", Address: "10.1.2.3", Port: 8080, TTL: 40 * time.Millisecond, Health: health,
	})
	check := reg.ID() + "-heartbeat"

	mu.Lock()
	failing = true
	mu.Unlock()

	waitFor(t, "the check to go critical", func() bool {
		return a.status(check) == api.HealthCritical
	})

	// an unhealthy instance stays registered, so it recovers on its own
	if a.service(reg.ID()) == nil {
		t.Fatal("the instance was deregistered instead of marked critical")
	}
	mu.Lock()
	failing = false
	mu.Unlock()

	waitFor(t, "the check to recover", func() bool {
		return a.status(check) == api.HealthPassing
	})
}

func TestHeartbeatRegistersAgainWhenTheAgentForgot(t *testing.T) {
	a, client := newAgent(t)

	reg := register(t, client, registry.Service{
		Name: "api", Address: "10.1.2.3", Port: 8080, TTL: 40 * time.Millisecond,
	})

	registers, _ := a.counts()
	a.forget()

	waitFor(t, "the instance to register again", func() bool {
		again, _ := a.counts()
		return again > registers
	})
	if a.service(reg.ID()) == nil {
		t.Fatal("the instance is not back in the catalog")
	}
}

func TestCloseTakesTheInstanceOutOfTheCatalog(t *testing.T) {
	a, client := newAgent(t)

	reg, err := registry.Register(t.Context(), client, registry.Service{
		Name: "api", Address: "10.1.2.3", Port: 8080, Logger: quiet(),
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := reg.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if a.service(reg.ID()) != nil {
		t.Error("the instance is still in the catalog after Close")
	}
	if err := reg.Close(); err != nil {
		t.Errorf("second close: %v, want it to be a no-op", err)
	}
}

func TestRegisteredServiceIsDiscoverableThroughTheMiddleware(t *testing.T) {
	_, client := newAgent(t)

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
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp, err := call.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if !strings.Contains(string(body), "host api") {
			t.Errorf("backend saw %q, want the service name as the Host header", body)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(replicas) != 2 {
		t.Errorf("only %v were reached, want both replicas load balanced across", replicas)
	}
}
func TestUnhealthyServiceIsNotDiscoverable(t *testing.T) {
	_, client := newAgent(t)

	register(t, client, registry.Service{
		Name: "api", Address: "10.1.2.3", Port: 8080, TTL: 40 * time.Millisecond,
		Health: func(context.Context) error { return fmt.Errorf("not ready") },
	})

	call := caller.New(http.Client{}, consul.Resolve(client, time.Millisecond))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://api/things", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := call.Do(req); err == nil {
		t.Fatal("the unhealthy replica was routed to")
	}
}

func split(t *testing.T, rawURL string) (host string, port int) {
	t.Helper()

	var err error
	host, rawPort, _ := strings.Cut(strings.TrimPrefix(rawURL, "http://"), ":")
	if _, err = fmt.Sscanf(rawPort, "%d", &port); err != nil {
		t.Fatalf("port of %q: %v", rawURL, err)
	}
	return host, port
}
