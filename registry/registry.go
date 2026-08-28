package registry

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hashicorp/consul/api"
)

const DefaultTTL = 15 * time.Second

const deregisterAfter = time.Minute

type Service struct {
	Name string

	// Port is the port this instance serves on.
	Port int

	// Address is where callers reach this instance. Detected from the process's own
	// network when empty.
	Address string

	Tags []string
	Meta map[string]string

	// TTL bounds how long the catalog keeps routing to this instance after it stops
	// heartbeating.
	TTL time.Duration

	// Health, when set, is called before every heartbeat. A non-nil error marks this
	// instance critical, which takes it out of discovery without deregistering it, so
	// it comes back on its own once the error clears.
	Health func(context.Context) error

	// Logger reports heartbeat failures, the only errors raised after Register returns.
	// Defaults to slog.Default().
	Logger *slog.Logger
}

// Registration is a live registration, kept alive by a background heartbeat until Close.
type Registration struct {
	agent   *api.Agent
	service *api.AgentServiceRegistration
	health  func(context.Context) error
	log     *slog.Logger
	ttl     time.Duration

	cancel context.CancelFunc
	done   chan struct{}
	closed sync.Once
}

// Register adds this instance to the catalog and starts heartbeating for it.
func Register(ctx context.Context, client *api.Client, svc Service) (*Registration, error) {
	if svc.Name == "" {
		return nil, fmt.Errorf("registry: service name is required")
	}
	if svc.Port <= 0 {
		return nil, fmt.Errorf("registry: service %q needs a port", svc.Name)
	}

	address := svc.Address
	if address == "" {
		var err error
		if address, err = localAddress(); err != nil {
			return nil, fmt.Errorf("registry: service %q: %w", svc.Name, err)
		}
	}

	r := &Registration{
		agent:  client.Agent(),
		health: svc.Health,
		log:    svc.Logger,
		ttl:    svc.TTL,
		done:   make(chan struct{}),
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	if r.ttl <= 0 {
		r.ttl = DefaultTTL
	}

	id := fmt.Sprintf("%s-%s-%d", svc.Name, address, svc.Port)
	r.service = &api.AgentServiceRegistration{
		ID:      id,
		Name:    svc.Name,
		Address: address,
		Port:    svc.Port,
		Tags:    svc.Tags,
		Meta:    svc.Meta,
		Check: &api.AgentServiceCheck{
			CheckID:                        id + "-heartbeat",
			Name:                           "heartbeat",
			TTL:                            r.ttl.String(),
			DeregisterCriticalServiceAfter: deregisterAfter.String(),
		},
	}
	if err := r.register(ctx); err != nil {
		return nil, err
	}

	loop, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r.cancel = cancel
	go r.heartbeat(loop)

	return r, nil
}

// ID is the catalog ID of this instance, unique among the replicas of the service.
func (r *Registration) ID() string { return r.service.ID }

// Close stops the heartbeat and removes this instance from the catalog.
func (r *Registration) Close() error {
	var err error
	r.closed.Do(func() {
		r.cancel()
		<-r.done

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if derr := r.agent.ServiceDeregisterOpts(r.service.ID, query(ctx)); derr != nil {
			err = fmt.Errorf("registry: failed to deregister %q: %w", r.service.ID, derr)
		}
	})
	return err
}

func (r *Registration) heartbeat(ctx context.Context) {
	defer close(r.done)

	ticker := time.NewTicker(r.ttl / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.beat(ctx); err != nil {
				r.log.ErrorContext(ctx, "consul heartbeat failed", "service", r.service.Name, "id", r.service.ID, "error", err)
			}
		}
	}
}

func (r *Registration) beat(ctx context.Context) error {
	status, output := r.probe(ctx)
	err := r.agent.UpdateTTLOpts(r.service.Check.CheckID, output, status, query(ctx))
	if err == nil {
		return nil
	}

	if rerr := r.register(ctx); rerr != nil {
		return fmt.Errorf("%w (re-registering: %w)", err, rerr)
	}
	return nil
}

func (r *Registration) register(ctx context.Context) error {
	r.service.Check.Status, r.service.Check.Notes = r.probe(ctx)

	opts := api.ServiceRegisterOpts{ReplaceExistingChecks: true}.WithContext(ctx)
	if err := r.agent.ServiceRegisterOpts(r.service, opts); err != nil {
		return fmt.Errorf("registry: failed to register %q: %w", r.service.ID, err)
	}
	return nil
}

func (r *Registration) probe(ctx context.Context) (status, output string) {
	if r.health == nil {
		return api.HealthPassing, "alive"
	}
	if err := r.health(ctx); err != nil {
		return api.HealthCritical, err.Error()
	}
	return api.HealthPassing, "healthy"
}

func query(ctx context.Context) *api.QueryOptions {
	return (&api.QueryOptions{}).WithContext(ctx)
}
