// Package ports hands out host ports from a bounded pool, one per Minecraft
// server.
package ports

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
)

// ErrPoolExhausted means every port in the configured range is claimed.
var ErrPoolExhausted = errors.New("no free ports in the configured range")

// UsedPortsFunc reports the ports already claimed by existing server records.
// The allocator asks the store rather than caching, so a port freed by a
// deletion becomes available again without a restart.
type UsedPortsFunc func(ctx context.Context) (map[int]bool, error)

// Allocator assigns host ports from [Start, End].
type Allocator struct {
	start, end int
	usedPorts  UsedPortsFunc

	// probeHost enables the "can anything bind this port?" check. It must be
	// disabled when this process runs inside a container: the probe would
	// then test the container's own network namespace, not the Docker host
	// where the game containers actually publish their ports.
	probeHost bool

	// mu serialises Allocate so two concurrent create requests cannot pick the
	// same port between the read and the insert. The UNIQUE constraint on
	// servers.port is the backstop if this process is ever run twice.
	mu sync.Mutex
}

// New builds an allocator over the inclusive range [start, end]. Set
// probeHost only when this process shares the Docker host's network
// namespace; see the field comment.
func New(start, end int, used UsedPortsFunc, probeHost bool) *Allocator {
	return &Allocator{start: start, end: end, usedPorts: used, probeHost: probeHost}
}

// Allocate returns the lowest free port in the range. A port is free when no
// server record claims it and, when host probing is enabled, nothing is
// already listening on it — the second check catches ports used by services
// outside this platform.
func (a *Allocator) Allocate(ctx context.Context) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	claimed, err := a.usedPorts(ctx)
	if err != nil {
		return 0, fmt.Errorf("read claimed ports: %w", err)
	}

	for p := a.start; p <= a.end; p++ {
		if claimed[p] {
			continue
		}
		if a.probeHost && !hostPortFree(p) {
			continue
		}
		return p, nil
	}
	return 0, ErrPoolExhausted
}

// hostPortFree reports whether the host can currently bind the TCP port. This
// is advisory: the port could be taken between this check and the container
// starting, in which case Docker returns a bind error we surface as a failed
// start.
func hostPortFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}
