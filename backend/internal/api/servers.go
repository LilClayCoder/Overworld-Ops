package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/calebgrabowski/overworld-ops/backend/internal/docker"
	"github.com/calebgrabowski/overworld-ops/backend/internal/ports"
	"github.com/calebgrabowski/overworld-ops/backend/internal/store"
)

// serverView is a Server record plus live container state, which is what the
// dashboard actually renders.
type serverView struct {
	*store.Server
	// Address is the host:port players connect to.
	Address string `json:"address"`
	// ContainerStatus and Health come from Docker, not the database, so a
	// container that died on its own shows up as stopped immediately.
	ContainerStatus string `json:"containerStatus,omitempty"`
	Health          string `json:"health,omitempty"`
}

// handleListServers returns the caller's servers. Admins see everything.
func (s *Server) handleListServers(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	ownerFilter := user.ID
	if user.IsAdmin {
		ownerFilter = "" // no filter
	}

	servers, err := s.store.ListServers(r.Context(), ownerFilter)
	if err != nil {
		slog.Error("list servers", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list servers")
		return
	}

	views := make([]*serverView, 0, len(servers))
	for _, srv := range servers {
		views = append(views, s.view(r.Context(), srv))
	}
	writeJSON(w, http.StatusOK, views)
}

// handleGetServer returns one server.
func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.lookup(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.view(r.Context(), srv))
}

// handleCreateServer allocates a port, writes the record, then creates the
// container. The record is written first so a failed container create leaves a
// visible server in the error state rather than a silently leaked port.
func (s *Server) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	var in store.CreateServerInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Resource governance: refuse to exceed the configured server cap.
	count, err := s.store.CountServers(r.Context())
	if err != nil {
		slog.Error("count servers", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create server")
		return
	}
	if count >= s.cfg.MaxServers {
		writeError(w, http.StatusConflict, "server limit reached")
		return
	}

	if in.ModpackProvider == store.ModpackCurseForge && s.cfg.CurseForgeAPIKey == "" {
		writeError(w, http.StatusBadRequest,
			"CurseForge modpacks need OWO_CURSEFORGE_API_KEY to be configured")
		return
	}

	port, err := s.ports.Allocate(r.Context())
	if err != nil {
		if errors.Is(err, ports.ErrPoolExhausted) {
			writeError(w, http.StatusConflict, "no free ports available")
			return
		}
		slog.Error("allocate port", "error", err)
		writeError(w, http.StatusInternalServerError, "could not allocate a port")
		return
	}

	memory := in.Memory
	if memory == "" {
		memory = s.cfg.DefaultMemory
	}
	cpu := in.CPULimit
	if cpu == 0 {
		cpu = s.cfg.DefaultCPULimit
	}

	srv := &store.Server{
		Name:            in.Name,
		OwnerID:         user.ID,
		Type:            in.Type,
		Version:         in.Version,
		ModpackProvider: in.ModpackProvider,
		ModpackID:       in.ModpackID,
		Port:            port,
		Memory:          memory,
		CPULimit:        cpu,
		Status:          store.StatusCreating,
	}

	if err := s.store.CreateServer(r.Context(), srv); err != nil {
		if errors.Is(err, store.ErrPortTaken) {
			// Lost a race with a concurrent create; the client can retry.
			writeError(w, http.StatusConflict, "port was just taken, please retry")
			return
		}
		slog.Error("create server record", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create server")
		return
	}

	containerID, err := s.docker.CreateServer(r.Context(), s.specFor(srv))
	if err != nil {
		slog.Error("create container", "server", srv.ID, "error", err)
		// Keep the record so the failure is visible in the UI and the port is
		// not silently reused while a half-built container may exist.
		_ = s.store.SetStatus(r.Context(), srv.ID, store.StatusError, err.Error())
		srv.Status, srv.StatusMessage = store.StatusError, err.Error()
		writeJSON(w, http.StatusInternalServerError, s.view(r.Context(), srv))
		return
	}

	srv.ContainerID = containerID
	srv.Status = store.StatusStopped
	if err := s.store.UpdateServer(r.Context(), srv); err != nil {
		slog.Error("save container id", "server", srv.ID, "error", err)
	}

	writeJSON(w, http.StatusCreated, s.view(r.Context(), srv))
}

// updateServerInput is the subset of fields an owner may change after
// creation. Type, version and modpack changes require recreating the
// container, which is why they are not editable here yet.
type updateServerInput struct {
	Name *string `json:"name,omitempty"`
}

// handleUpdateServer renames a server.
func (s *Server) handleUpdateServer(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.lookup(w, r)
	if !ok {
		return
	}

	var in updateServerInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" || len(name) > 64 {
			writeError(w, http.StatusBadRequest, "name must be 1-64 characters")
			return
		}
		srv.Name = name
	}

	if err := s.store.UpdateServer(r.Context(), srv); err != nil {
		slog.Error("update server", "server", srv.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not update server")
		return
	}
	writeJSON(w, http.StatusOK, s.view(r.Context(), srv))
}

// handleStartServer starts the container, creating it first if it is missing
// (for example after a manual `docker rm`). World data lives in a named volume
// keyed on the server ID, so a recreated container picks the world back up.
func (s *Server) handleStartServer(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.lookup(w, r)
	if !ok {
		return
	}

	if srv.ContainerID == "" {
		if err := s.recreateContainer(r.Context(), srv); err != nil {
			s.failServer(r.Context(), srv, "create container", err)
			writeError(w, http.StatusInternalServerError, "could not create container")
			return
		}
	}

	err := s.docker.StartServer(r.Context(), srv.ContainerID)
	if errors.Is(err, docker.ErrNotFound) {
		// The record points at a container that no longer exists. Rebuild it
		// rather than leaving the server permanently unstartable.
		slog.Warn("container missing, recreating", "server", srv.ID)
		if err = s.recreateContainer(r.Context(), srv); err == nil {
			err = s.docker.StartServer(r.Context(), srv.ContainerID)
		}
	}
	if err != nil {
		s.failServer(r.Context(), srv, "start container", err)
		writeError(w, http.StatusInternalServerError, "could not start server")
		return
	}

	// Starting, not running: a modpack can take minutes to reach the point
	// where it accepts connections. The UI polls until health goes healthy.
	srv.Status, srv.StatusMessage = store.StatusStarting, ""
	if err := s.store.SetStatus(r.Context(), srv.ID, srv.Status, ""); err != nil {
		slog.Error("set status", "server", srv.ID, "error", err)
	}
	_ = s.store.TouchActivity(r.Context(), srv.ID)

	writeJSON(w, http.StatusOK, s.view(r.Context(), srv))
}

// handleStopServer stops the container, giving the world time to save.
func (s *Server) handleStopServer(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if srv.ContainerID == "" {
		writeError(w, http.StatusConflict, "server has no container")
		return
	}

	_ = s.store.SetStatus(r.Context(), srv.ID, store.StatusStopping, "")

	timeout := int(s.cfg.StopTimeout.Seconds())
	if err := s.docker.StopServer(r.Context(), srv.ContainerID, timeout); err != nil {
		if !errors.Is(err, docker.ErrNotFound) {
			s.failServer(r.Context(), srv, "stop container", err)
			writeError(w, http.StatusInternalServerError, "could not stop server")
			return
		}
	}

	srv.Status, srv.StatusMessage = store.StatusStopped, ""
	if err := s.store.SetStatus(r.Context(), srv.ID, srv.Status, ""); err != nil {
		slog.Error("set status", "server", srv.ID, "error", err)
	}
	writeJSON(w, http.StatusOK, s.view(r.Context(), srv))
}

// handleDeleteServer removes the container and the record. World data survives
// unless ?deleteData=true is passed, so an accidental delete is recoverable by
// recreating the server against the same volume.
func (s *Server) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.lookup(w, r)
	if !ok {
		return
	}

	deleteData := r.URL.Query().Get("deleteData") == "true"

	if srv.ContainerID != "" {
		err := s.docker.RemoveServer(r.Context(), srv.ContainerID, srv.VolumeName(), deleteData)
		if err != nil && !errors.Is(err, docker.ErrNotFound) {
			s.failServer(r.Context(), srv, "remove container", err)
			writeError(w, http.StatusInternalServerError, "could not remove container")
			return
		}
	}

	if err := s.store.DeleteServer(r.Context(), srv.ID); err != nil {
		slog.Error("delete server record", "server", srv.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete server")
		return
	}

	slog.Info("deleted server", "server", srv.ID, "name", srv.Name, "dataDeleted", deleteData)
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

// lookup resolves the {id} path value and enforces ownership. It writes the
// error response itself; ok is false when the caller should stop.
func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (*store.Server, bool) {
	id := r.PathValue("id")
	srv, err := s.store.ServerByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "server not found")
			return nil, false
		}
		slog.Error("load server", "server", id, "error", err)
		writeError(w, http.StatusInternalServerError, "could not load server")
		return nil, false
	}

	if !ownsServer(userFrom(r.Context()), srv) {
		// 404 rather than 403 so the API does not confirm that a server ID
		// belonging to somebody else exists.
		writeError(w, http.StatusNotFound, "server not found")
		return nil, false
	}
	return srv, true
}

// recreateContainer builds a fresh container for srv and records its ID. The
// named volume is reused, so the world survives.
func (s *Server) recreateContainer(ctx context.Context, srv *store.Server) error {
	// Clear any stale container of the same name first, or the create fails on
	// a name conflict.
	if srv.ContainerID != "" {
		_ = s.docker.RemoveServer(ctx, srv.ContainerID, srv.VolumeName(), false)
	}

	id, err := s.docker.CreateServer(ctx, s.specFor(srv))
	if err != nil {
		return err
	}

	srv.ContainerID = id
	if err := s.store.SetContainerID(ctx, srv.ID, id); err != nil {
		slog.Error("save container id", "server", srv.ID, "error", err)
	}
	return nil
}

// specFor translates a stored server into a Docker container spec.
func (s *Server) specFor(srv *store.Server) docker.Spec {
	spec := docker.Spec{
		ServerID:      srv.ID,
		OwnerID:       srv.OwnerID,
		ContainerName: srv.ContainerName(),
		VolumeName:    srv.VolumeName(),
		Type:          string(srv.Type),
		Version:       srv.Version,
		HostPort:      srv.Port,
		Memory:        srv.Memory,
		CPULimit:      srv.CPULimit,
		ExtraEnv:      map[string]string{"MOTD": srv.Name},
	}

	switch srv.ModpackProvider {
	case store.ModpackModrinth:
		spec.ModrinthModpack = srv.ModpackID
	case store.ModpackCurseForge:
		spec.CurseForgeSlug = srv.ModpackID
		spec.CurseForgeAPIKey = s.cfg.CurseForgeAPIKey
	}
	return spec
}

// view decorates a record with live container state. Docker failures are not
// fatal here: the record is still worth showing.
func (s *Server) view(ctx context.Context, srv *store.Server) *serverView {
	v := &serverView{Server: srv, Address: s.cfg.PublicHost(srv.Port)}
	if srv.ContainerID == "" {
		return v
	}

	// Bound the inspect so one wedged container cannot stall a list request.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	state, err := s.docker.InspectServer(ctx, srv.ContainerID)
	if err != nil {
		if !errors.Is(err, docker.ErrNotFound) {
			slog.Warn("inspect container", "server", srv.ID, "error", err)
		}
		return v
	}

	v.ContainerStatus = state.Status
	v.Health = state.Health
	return v
}

// failServer records a lifecycle failure on the record and logs it.
func (s *Server) failServer(ctx context.Context, srv *store.Server, op string, err error) {
	slog.Error(op, "server", srv.ID, "error", err)
	if setErr := s.store.SetStatus(ctx, srv.ID, store.StatusError, err.Error()); setErr != nil {
		slog.Error("set error status", "server", srv.ID, "error", setErr)
	}
}
