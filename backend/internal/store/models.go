// Package store holds the server registry: the persistent record of every
// Minecraft server the platform knows about, plus the users who own them.
package store

import (
	"fmt"
	"strings"
	"time"
)

// ServerType is the modloader a server runs. The values match the TYPE
// environment variable understood by the itzg/minecraft-server image.
type ServerType string

const (
	TypeVanilla  ServerType = "VANILLA"
	TypeForge    ServerType = "FORGE"
	TypeFabric   ServerType = "FABRIC"
	TypeNeoForge ServerType = "NEOFORGE"
)

// Valid reports whether t is a modloader we support.
func (t ServerType) Valid() bool {
	switch t {
	case TypeVanilla, TypeForge, TypeFabric, TypeNeoForge:
		return true
	}
	return false
}

// Status is the lifecycle state of a server record. It mirrors the container
// state but is stored so the UI can render without hitting Docker.
type Status string

const (
	// StatusCreating means the record exists but the container is not built yet.
	StatusCreating Status = "creating"
	// StatusStopped means the container exists but is not running.
	StatusStopped Status = "stopped"
	// StatusStarting covers the window between `docker start` and the server
	// answering on its port. Modpacks can sit here for minutes.
	StatusStarting Status = "starting"
	// StatusRunning means the container is up.
	StatusRunning Status = "running"
	// StatusStopping is a graceful shutdown in progress.
	StatusStopping Status = "stopping"
	// StatusError means the last lifecycle operation failed; see StatusMessage.
	StatusError Status = "error"
)

// ModpackProvider identifies where a modpack ID should be resolved from.
type ModpackProvider string

const (
	ModpackNone       ModpackProvider = ""
	ModpackCurseForge ModpackProvider = "curseforge"
	ModpackModrinth   ModpackProvider = "modrinth"
)

// Server is one Minecraft server: a database record plus the container that
// backs it.
type Server struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OwnerID string `json:"ownerId"`

	Type    ServerType `json:"type"`
	Version string     `json:"version"` // "1.21.1", or "LATEST"

	// ModpackProvider and ModpackID are empty for a plain vanilla/loader
	// server. When set they are passed through to itzg, which downloads and
	// installs the pack on first boot.
	ModpackProvider ModpackProvider `json:"modpackProvider,omitempty"`
	ModpackID       string          `json:"modpackId,omitempty"`

	// Port is the host port forwarded to the container's 25565.
	Port int `json:"port"`

	// Memory is the JVM heap, e.g. "4G". CPULimit is in fractional cores.
	Memory   string  `json:"memory"`
	CPULimit float64 `json:"cpuLimit"`

	Status        Status `json:"status"`
	StatusMessage string `json:"statusMessage,omitempty"`

	// ContainerID is the Docker container backing this server, empty until
	// the container is created.
	ContainerID string `json:"containerId,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// LastActiveAt is bumped whenever a player is seen connected; the idle
	// reaper compares it against the configured idle timeout.
	LastActiveAt *time.Time `json:"lastActiveAt,omitempty"`
}

// ContainerName is the deterministic Docker name for this server. Deriving it
// from the ID (rather than the user-supplied name) keeps it collision-free and
// lets us find orphaned containers after a database restore.
func (s *Server) ContainerName() string {
	return "owo-mc-" + s.ID
}

// VolumeName is the named Docker volume holding the world data.
func (s *Server) VolumeName() string {
	return "owo-data-" + s.ID
}

// User is someone who can log in and own servers.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	IsAdmin      bool      `json:"isAdmin"`
	CreatedAt    time.Time `json:"createdAt"`
}

// CreateServerInput is the validated payload for a new server.
type CreateServerInput struct {
	Name            string          `json:"name"`
	Type            ServerType      `json:"type"`
	Version         string          `json:"version"`
	ModpackProvider ModpackProvider `json:"modpackProvider,omitempty"`
	ModpackID       string          `json:"modpackId,omitempty"`
	Memory          string          `json:"memory,omitempty"`
	CPULimit        float64         `json:"cpuLimit,omitempty"`
}

// Validate checks user-supplied fields before anything touches Docker.
func (in *CreateServerInput) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(in.Name) > 64 {
		return fmt.Errorf("name must be 64 characters or fewer")
	}
	if in.Type == "" {
		in.Type = TypeVanilla
	}
	if !in.Type.Valid() {
		return fmt.Errorf("unsupported server type %q", in.Type)
	}
	if strings.TrimSpace(in.Version) == "" {
		in.Version = "LATEST"
	}
	switch in.ModpackProvider {
	case ModpackNone, ModpackCurseForge, ModpackModrinth:
	default:
		return fmt.Errorf("unsupported modpack provider %q", in.ModpackProvider)
	}
	if in.ModpackProvider != ModpackNone && strings.TrimSpace(in.ModpackID) == "" {
		return fmt.Errorf("modpackId is required when modpackProvider is set")
	}
	if in.CPULimit < 0 {
		return fmt.Errorf("cpuLimit must not be negative")
	}
	return nil
}
