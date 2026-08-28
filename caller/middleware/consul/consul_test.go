package consul_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/draincloud/callpack/caller"
	"github.com/draincloud/callpack/caller/middleware"
	"github.com/draincloud/callpack/caller/middleware/consul"
	"github.com/hashicorp/consul/api"
)

type catalog struct {
	mu        sync.Mutex
	instances map[string][]*api.ServiceEntry
	lookups   int64
}

func (c *catalog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&c.lookups, 1)
	name := r.PathValue("name")

	c.mu.Lock()
	entries := c.instances[name]
	c.mu.Unlock()
	if entries == nil {
		entries = []*api.ServiceEntry{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

func (c *catalog) set(name string, entries ...*api.ServiceEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.instances[name] = entries
}

func instance(nodeAddr, serviceAddr string, port int) *api.ServiceEntry {
	return &api.ServiceEntry{
		Node:    &api.Node{Node: "node", Address: nodeAddr},
		Service: &api.AgentService{Service: "api", Address: serviceAddr, Port: port},
	}
}

type recorder struct {
	mu    sync.Mutex
	seen  []*http.Request
	calls int64
}

func (t *recorder) RoundTrip(r *http.Request) (*http.Response, error) {
	atomic.AddInt64(&t.calls, 1)
	t.mu.Lock()
	t.seen = append(t.seen, r)
	t.mu.Unlock()
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: r}, nil
}

func (t *recorder) last() *http.Request {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seen[len(t.seen)-1]
}

func setup(t *testing.T, ttl time.Duration) (*catalog, *recorder, http.RoundTripper) {
	t.Helper()

	cat := &catalog{instances: map[string][]*api.ServiceEntry{}}
	mux := http.NewServeMux()
	mux.Handle("/v1/health/service/{name}", cat)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := api.NewClient(&api.Config{Address: srv.URL})
	if err != nil {
		t.Fatalf("api.NewClient: %v", err)
	}

	rec := &recorder{}
	return cat, rec, consul.Resolve(client, ttl)(rec)
}

func request(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	return req
}

func TestResolveRewritesTargetAndKeepsServiceNameAsHost(t *testing.T) {
	cat, rec, rt := setup(t, 0)
	cat.set("api", instance("10.0.0.1", "10.0.0.11", 8080))

	req := request(t, "http://api/v1/things?q=1")
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	sent := rec.last()
	if got, want := sent.URL.Host, "10.0.0.11:8080"; got != want {
		t.Errorf("target host = %q, want %q", got, want)
	}
	if got, want := sent.Host, "api"; got != want {
		t.Errorf("Host header = %q, want %q", got, want)
	}
	if got, want := sent.URL.RequestURI(), "/v1/things?q=1"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got, want := req.URL.Host, "api"; got != want {
		t.Errorf("caller's request was mutated: host = %q, want %q", got, want)
	}
}

func TestResolveFallsBackToNodeAddressAndIgnoresURLPort(t *testing.T) {
	cat, rec, rt := setup(t, 0)
	cat.set("api", instance("10.0.0.1", "", 9000)) // no service address: the node's is used

	if _, err := rt.RoundTrip(request(t, "http://api:1234/x")); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if got, want := rec.last().URL.Host, "10.0.0.1:9000"; got != want {
		t.Errorf("target host = %q, want %q", got, want)
	}
}

func TestResolveCachesForTTL(t *testing.T) {
	cat, _, rt := setup(t, 200*time.Millisecond)
	cat.set("api", instance("10.0.0.1", "10.0.0.11", 8080))

	for range 5 {
		if _, err := rt.RoundTrip(request(t, "http://api/x")); err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
	}
	if got := atomic.LoadInt64(&cat.lookups); got != 1 {
		t.Errorf("lookups within the TTL = %d, want 1", got)
	}

	time.Sleep(250 * time.Millisecond)
	if _, err := rt.RoundTrip(request(t, "http://api/x")); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if got := atomic.LoadInt64(&cat.lookups); got != 2 {
		t.Errorf("lookups after the TTL = %d, want 2", got)
	}
}

func TestResolveCachesPerService(t *testing.T) {
	cat, rec, rt := setup(t, time.Minute)
	cat.set("api", instance("10.0.0.1", "10.0.0.11", 8080))
	cat.set("billing", instance("10.0.0.2", "10.0.0.22", 9090))

	for _, name := range []string{"api", "billing", "api", "billing"} {
		if _, err := rt.RoundTrip(request(t, "http://"+name+"/x")); err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
	}
	if got := atomic.LoadInt64(&cat.lookups); got != 2 {
		t.Errorf("lookups = %d, want 2 (one per service)", got)
	}
	if got, want := rec.last().URL.Host, "10.0.0.22:9090"; got != want {
		t.Errorf("billing resolved to %q, want %q", got, want)
	}
}

func TestResolveRoundRobinsAcrossInstances(t *testing.T) {
	cat, rec, rt := setup(t, time.Minute)
	cat.set("api",
		instance("10.0.0.1", "10.0.0.11", 8080),
		instance("10.0.0.2", "10.0.0.12", 8080),
		instance("10.0.0.3", "10.0.0.13", 8080),
	)

	var got []string
	for range 6 {
		if _, err := rt.RoundTrip(request(t, "http://api/x")); err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		got = append(got, rec.last().URL.Host)
	}

	want := []string{
		"10.0.0.11:8080", "10.0.0.12:8080", "10.0.0.13:8080",
		"10.0.0.11:8080", "10.0.0.12:8080", "10.0.0.13:8080",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("instance order = %v, want %v", got, want)
		}
	}
}

func TestResolveFailsWithoutHealthyInstance(t *testing.T) {
	cat, rec, rt := setup(t, time.Minute)
	cat.set("api") // registered, nothing passing

	if _, err := rt.RoundTrip(request(t, "http://api/x")); err == nil {
		t.Fatal("RoundTrip succeeded, want an error")
	}
	if got := atomic.LoadInt64(&rec.calls); got != 0 {
		t.Errorf("transport calls = %d, want 0: the request must not be sent unresolved", got)
	}

	cat.set("api", instance("10.0.0.1", "10.0.0.11", 8080))
	if _, err := rt.RoundTrip(request(t, "http://api/x")); err != nil {
		t.Fatalf("RoundTrip after recovery: %v", err)
	}
	if got, want := rec.last().URL.Host, "10.0.0.11:8080"; got != want {
		t.Errorf("target host = %q, want %q", got, want)
	}
}

func TestResolveRejectsRequestWithoutHost(t *testing.T) {
	_, _, rt := setup(t, 0)

	req := &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "http", Path: "/x"}, Header: http.Header{}}
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip succeeded, want an error")
	}
}

func TestResolveMakesOneLookupForConcurrentRequests(t *testing.T) {
	cat, _, rt := setup(t, time.Minute)
	cat.set("api", instance("10.0.0.1", "10.0.0.11", 8080))

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := rt.RoundTrip(request(t, "http://api/x")); err != nil {
				t.Errorf("RoundTrip: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&cat.lookups); got != 1 {
		t.Errorf("lookups = %d, want 1: concurrent requests must share one lookup", got)
	}
}

// TestResolveThroughCaller drives the middleware the way a user does: through a Caller, over a real
// transport, to a backend registered in the catalog.
func TestResolveThroughCaller(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("served " + r.Host + r.URL.Path))
	}))
	t.Cleanup(backend.Close)

	host, port, err := net.SplitHostPort(strings.TrimPrefix(backend.URL, "http://"))
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	backendPort, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}

	cat := &catalog{instances: map[string][]*api.ServiceEntry{}}
	mux := http.NewServeMux()
	mux.Handle("/v1/health/service/{name}", cat)
	consulSrv := httptest.NewServer(mux)
	t.Cleanup(consulSrv.Close)
	cat.set("api", instance(host, "", backendPort))

	client, err := api.NewClient(&api.Config{Address: consulSrv.URL})
	if err != nil {
		t.Fatalf("api.NewClient: %v", err)
	}

	c := caller.New(http.Client{}, consul.Resolve(client, 0), middleware.Header("X-Trace", "abc"))

	resp, err := c.Do(request(t, "http://api/things"))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if got, want := string(body), "served api/things"; got != want {
		t.Errorf("body = %q, want %q (the backend must see the service name as its Host)", got, want)
	}
}
