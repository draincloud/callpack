package gateway

import (
	"context"

	servicediscovery "github.com/draincloud/callpack/client/service_discovery"
)

type Gateway interface {
	GetInstances(ctx context.Context, name string) ([]servicediscovery.ServiceInstance, error)
}
