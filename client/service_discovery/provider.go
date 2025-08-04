package servicediscovery

import "context"

type ServiceInstance struct {
	Name     string
	Address  string
	Metadata map[string]string
	LBPolicy any // TODO
	// ...
}

type Provider interface {
	Discover(ctx context.Context, name string) ([]ServiceInstance, error)
}
