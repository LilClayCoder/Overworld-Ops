package ports

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
)

func staticUsed(used map[int]bool) UsedPortsFunc {
	return func(context.Context) (map[int]bool, error) { return used, nil }
}

func TestAllocatePicksLowestFree(t *testing.T) {
	a := New(30000, 30010, staticUsed(map[int]bool{30000: true, 30001: true}), true)

	got, err := a.Allocate(context.Background())
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got != 30002 {
		t.Errorf("Allocate = %d, want 30002", got)
	}
}

func TestAllocateExhausted(t *testing.T) {
	a := New(30020, 30021, staticUsed(map[int]bool{30020: true, 30021: true}), true)

	if _, err := a.Allocate(context.Background()); !errors.Is(err, ErrPoolExhausted) {
		t.Errorf("Allocate on full pool = %v, want ErrPoolExhausted", err)
	}
}

func TestAllocateSkipsPortsInUseOnHost(t *testing.T) {
	// Occupy a port with something outside the platform's registry; the
	// allocator must skip it rather than hand out a port Docker cannot bind.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skipf("cannot bind a test listener: %v", err)
	}
	defer ln.Close()

	busy := ln.Addr().(*net.TCPAddr).Port
	a := New(busy, busy+1, staticUsed(map[int]bool{}), true)

	got, err := a.Allocate(context.Background())
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got == busy {
		t.Errorf("Allocate returned port %d, which is already bound on the host", busy)
	}
}

func TestAllocateSkipsHostProbeWhenDisabled(t *testing.T) {
	// With probing off, a port bound on this host must still be handed out:
	// inside a container the local bind says nothing about the Docker host.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skipf("cannot bind a test listener: %v", err)
	}
	defer ln.Close()

	busy := ln.Addr().(*net.TCPAddr).Port
	a := New(busy, busy+1, staticUsed(map[int]bool{}), false)

	got, err := a.Allocate(context.Background())
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got != busy {
		t.Errorf("Allocate = %d, want %d (host probe should be skipped)", got, busy)
	}
}

func TestAllocatePropagatesStoreError(t *testing.T) {
	want := fmt.Errorf("database is down")
	a := New(30030, 30040, func(context.Context) (map[int]bool, error) { return nil, want }, true)

	if _, err := a.Allocate(context.Background()); !errors.Is(err, want) {
		t.Errorf("Allocate = %v, want it to wrap %v", err, want)
	}
}
