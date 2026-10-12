package nodes

import (
	"context"
	"net/http"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/store"
)

// PoolerDriver reaches pooler hosts' agents for the pooler manager and the
// arbiter (V3 §2.1).
type PoolerDriver struct{ S *Service }

// Hosts lists the registered pooler hosts.
func (d PoolerDriver) Hosts(ctx context.Context) ([]store.Node, error) {
	return store.New(d.S.db).PoolerHosts(ctx)
}

// Push sends a configuration bundle (PUT /v1/pooler/config).
func (d PoolerDriver) Push(ctx context.Context, n store.Node, b agentapi.PoolerBundle) (agentapi.PoolerStatus, error) {
	var st agentapi.PoolerStatus
	a, err := d.S.agentFor(n)
	if err != nil {
		return st, err
	}
	return st, a.do(ctx, http.MethodPut, agentapi.PathPoolerConfig, b, &st)
}

// Expect tells a host the newest generation (PUT /v1/pooler/expected)
// and returns its status.
func (d PoolerDriver) Expect(ctx context.Context, n store.Node, e agentapi.PoolerExpected) (agentapi.PoolerStatus, error) {
	var st agentapi.PoolerStatus
	a, err := d.S.agentFor(n)
	if err != nil {
		return st, err
	}
	return st, a.do(ctx, http.MethodPut, agentapi.PathPoolerExpected, e, &st)
}
