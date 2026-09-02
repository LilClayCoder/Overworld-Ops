package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// newTestStore opens a throwaway database on disk. A file rather than
// :memory: because the store pins MaxOpenConns to 1 and the schema must
// survive across calls the same way it does in production.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newTestUser(t *testing.T, st *Store, name string) *User {
	t.Helper()

	u, err := st.CreateUser(context.Background(), name, "hash", false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func TestUserRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if exists, err := st.AnyUserExists(ctx); err != nil || exists {
		t.Fatalf("AnyUserExists on empty db = %v, %v; want false, nil", exists, err)
	}

	created := newTestUser(t, st, "steve")

	fetched, err := st.UserByUsername(ctx, "steve")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	if fetched.ID != created.ID {
		t.Errorf("id = %q, want %q", fetched.ID, created.ID)
	}

	if exists, err := st.AnyUserExists(ctx); err != nil || !exists {
		t.Fatalf("AnyUserExists after insert = %v, %v; want true, nil", exists, err)
	}

	if _, err := st.UserByUsername(ctx, "alex"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing user error = %v, want ErrNotFound", err)
	}
}

func TestCreateServerAndList(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	owner := newTestUser(t, st, "steve")

	srv := &Server{
		Name: "SMP", OwnerID: owner.ID, Type: TypeFabric, Version: "1.21.1",
		Port: 25565, Memory: "4G", CPULimit: 2, Status: StatusCreating,
	}
	if err := st.CreateServer(ctx, srv); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	if srv.ID == "" {
		t.Error("CreateServer must assign an ID")
	}
	if srv.CreatedAt.IsZero() || srv.UpdatedAt.IsZero() {
		t.Error("CreateServer must set timestamps")
	}

	got, err := st.ServerByID(ctx, srv.ID)
	if err != nil {
		t.Fatalf("ServerByID: %v", err)
	}
	if got.Name != "SMP" || got.Type != TypeFabric || got.Port != 25565 {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.LastActiveAt != nil {
		t.Error("LastActiveAt should be nil until activity is recorded")
	}

	// A second owner's server must not show up in the first owner's list.
	other := newTestUser(t, st, "alex")
	if err := st.CreateServer(ctx, &Server{
		Name: "Theirs", OwnerID: other.ID, Type: TypeVanilla, Version: "LATEST",
		Port: 25566, Memory: "2G", Status: StatusStopped,
	}); err != nil {
		t.Fatalf("CreateServer for other owner: %v", err)
	}

	mine, err := st.ListServers(ctx, owner.ID)
	if err != nil {
		t.Fatalf("ListServers: %v", err)
	}
	if len(mine) != 1 || mine[0].ID != srv.ID {
		t.Errorf("owner scoping broken: got %d servers", len(mine))
	}

	all, err := st.ListServers(ctx, "")
	if err != nil {
		t.Fatalf("ListServers(all): %v", err)
	}
	if len(all) != 2 {
		t.Errorf("unscoped list = %d servers, want 2", len(all))
	}
}

func TestCreateServerRejectsDuplicatePort(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	owner := newTestUser(t, st, "steve")

	first := &Server{
		Name: "A", OwnerID: owner.ID, Type: TypeVanilla, Version: "LATEST",
		Port: 25565, Memory: "2G", Status: StatusStopped,
	}
	if err := st.CreateServer(ctx, first); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	// The UNIQUE constraint is the backstop against two servers binding the
	// same host port if the allocator ever races.
	second := &Server{
		Name: "B", OwnerID: owner.ID, Type: TypeVanilla, Version: "LATEST",
		Port: 25565, Memory: "2G", Status: StatusStopped,
	}
	if err := st.CreateServer(ctx, second); !errors.Is(err, ErrPortTaken) {
		t.Errorf("duplicate port error = %v, want ErrPortTaken", err)
	}
}

func TestUsedPortsAndCount(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	owner := newTestUser(t, st, "steve")

	for _, p := range []int{25565, 25567} {
		if err := st.CreateServer(ctx, &Server{
			Name: "s", OwnerID: owner.ID, Type: TypeVanilla, Version: "LATEST",
			Port: p, Memory: "2G", Status: StatusStopped,
		}); err != nil {
			t.Fatalf("CreateServer(%d): %v", p, err)
		}
	}

	used, err := st.UsedPorts(ctx)
	if err != nil {
		t.Fatalf("UsedPorts: %v", err)
	}
	if !used[25565] || !used[25567] || used[25566] {
		t.Errorf("UsedPorts = %v, want 25565 and 25567 only", used)
	}

	n, err := st.CountServers(ctx)
	if err != nil || n != 2 {
		t.Errorf("CountServers = %d, %v; want 2, nil", n, err)
	}
}

func TestStatusAndDelete(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	owner := newTestUser(t, st, "steve")

	srv := &Server{
		Name: "SMP", OwnerID: owner.ID, Type: TypeVanilla, Version: "LATEST",
		Port: 25565, Memory: "2G", Status: StatusCreating,
	}
	if err := st.CreateServer(ctx, srv); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	if err := st.SetStatus(ctx, srv.ID, StatusError, "boom"); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if err := st.SetContainerID(ctx, srv.ID, "abc123"); err != nil {
		t.Fatalf("SetContainerID: %v", err)
	}
	if err := st.TouchActivity(ctx, srv.ID); err != nil {
		t.Fatalf("TouchActivity: %v", err)
	}

	got, err := st.ServerByID(ctx, srv.ID)
	if err != nil {
		t.Fatalf("ServerByID: %v", err)
	}
	if got.Status != StatusError || got.StatusMessage != "boom" {
		t.Errorf("status = %q/%q, want error/boom", got.Status, got.StatusMessage)
	}
	if got.ContainerID != "abc123" {
		t.Errorf("containerID = %q, want abc123", got.ContainerID)
	}
	if got.LastActiveAt == nil {
		t.Error("TouchActivity must set LastActiveAt")
	}

	// Operations on a missing row must report ErrNotFound rather than
	// silently succeeding.
	if err := st.SetStatus(ctx, "nope", StatusStopped, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetStatus on missing server = %v, want ErrNotFound", err)
	}

	if err := st.DeleteServer(ctx, srv.ID); err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}
	if err := st.DeleteServer(ctx, srv.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second DeleteServer = %v, want ErrNotFound", err)
	}

	// The port must be free again once the record is gone.
	used, err := st.UsedPorts(ctx)
	if err != nil {
		t.Fatalf("UsedPorts: %v", err)
	}
	if used[25565] {
		t.Error("deleting a server must release its port")
	}
}

func TestServerNamesAreDerivedFromID(t *testing.T) {
	// Container and volume names must come from the ID, not the user-supplied
	// name, so two servers called "SMP" cannot collide.
	srv := &Server{ID: "abc-123", Name: "My SMP!"}
	if got := srv.ContainerName(); got != "owo-mc-abc-123" {
		t.Errorf("ContainerName = %q", got)
	}
	if got := srv.VolumeName(); got != "owo-data-abc-123" {
		t.Errorf("VolumeName = %q", got)
	}
}

func TestCreateServerInputValidate(t *testing.T) {
	tests := []struct {
		name    string
		in      CreateServerInput
		wantErr bool
	}{
		{"bare name defaults to vanilla/LATEST", CreateServerInput{Name: "SMP"}, false},
		{"empty name", CreateServerInput{Name: "   "}, true},
		{"unknown type", CreateServerInput{Name: "SMP", Type: "BUKKIT"}, true},
		{"fabric ok", CreateServerInput{Name: "SMP", Type: TypeFabric, Version: "1.21.1"}, false},
		{"unknown provider", CreateServerInput{Name: "SMP", ModpackProvider: "technic"}, true},
		{"provider without id", CreateServerInput{Name: "SMP", ModpackProvider: ModpackModrinth}, true},
		{"provider with id", CreateServerInput{Name: "SMP", ModpackProvider: ModpackModrinth, ModpackID: "cobblemon"}, false},
		{"negative cpu", CreateServerInput{Name: "SMP", CPULimit: -1}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && tc.in.Type == "" {
				t.Error("Validate must default the type")
			}
			if err == nil && tc.in.Version == "" {
				t.Error("Validate must default the version")
			}
		})
	}
}
