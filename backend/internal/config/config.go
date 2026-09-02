// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds every knob the server reads at startup.
type Config struct {
	// HTTPAddr is the listen address for the REST API.
	HTTPAddr string

	// DatabaseURL points at the SQLite file (or a Postgres DSN later).
	DatabaseURL string

	// DockerHost overrides the Docker Engine endpoint. Empty means "use the
	// environment" (DOCKER_HOST, or the local socket / named pipe).
	DockerHost string

	// MinecraftImage is the container image every game server runs.
	MinecraftImage string

	// DataRoot is the host directory under which per-server volumes live.
	DataRoot string

	// PortRangeStart / PortRangeEnd bound the host port pool handed out to
	// servers. 25565 is the vanilla Minecraft default.
	PortRangeStart int
	PortRangeEnd   int

	// MaxServers caps how many game servers may exist at once.
	MaxServers int

	// ProbeHostPorts makes the allocator check whether a candidate port can
	// actually be bound before handing it out. Turn it off when this process
	// runs inside a container, where the check would test the container's own
	// network namespace rather than the Docker host's.
	ProbeHostPorts bool

	// DefaultMemory is the JVM heap given to a server when the creator does
	// not specify one, e.g. "2G".
	DefaultMemory string

	// DefaultCPULimit is the fractional CPU cap per container, e.g. 2.0 for
	// two cores. Zero disables the cap.
	DefaultCPULimit float64

	// IdleTimeout stops a server with no players connected for this long.
	// Zero disables idle shutdown.
	IdleTimeout time.Duration

	// SessionSecret signs session cookies. Must be set outside of dev.
	SessionSecret string

	// SecureCookies marks session cookies Secure. Leave it off for plain
	// HTTP on a LAN; turn it on behind a TLS reverse proxy.
	SecureCookies bool

	// AllowRegistration lets anyone who can reach the UI create an account.
	// Handy while inviting friends, worth turning off afterwards.
	AllowRegistration bool

	// AdminUsername / AdminPassword seed the first account on a fresh
	// database. Ignored once any user exists.
	AdminUsername string
	AdminPassword string

	// CurseForgeAPIKey is required to auto-download CurseForge modpacks.
	// Modrinth needs no key.
	CurseForgeAPIKey string

	// CORSOrigin is the SvelteKit dev server origin allowed to call the API
	// with credentials. Empty disables CORS (same-origin production setup).
	CORSOrigin string

	// StopTimeout is how long a server gets to save its world on shutdown.
	StopTimeout time.Duration

	// PublicHostname is the address players type into the Minecraft client.
	// It is whatever resolves to this box from where the players are: a LAN
	// IP, a DDNS name, or a Tailscale hostname.
	PublicHostname string
}

// PublicHost formats the connection string shown on the dashboard. The port is
// omitted when it is 25565, because the Minecraft client assumes that default
// and players find a bare hostname easier to type.
func (c *Config) PublicHost(port int) string {
	if port == 25565 {
		return c.PublicHostname
	}
	return c.PublicHostname + ":" + strconv.Itoa(port)
}

// Load reads configuration from the environment, applying defaults that are
// sane for a single-box homelab install.
func Load() (*Config, error) {
	c := &Config{
		HTTPAddr:        env("OWO_HTTP_ADDR", ":8080"),
		DatabaseURL:     env("OWO_DATABASE_URL", "./data/overworld.db"),
		DockerHost:      env("OWO_DOCKER_HOST", ""),
		MinecraftImage:  env("OWO_MINECRAFT_IMAGE", "itzg/minecraft-server:latest"),
		DataRoot:        env("OWO_DATA_ROOT", "./data/servers"),
		DefaultMemory:   env("OWO_DEFAULT_MEMORY", "2G"),
		SessionSecret:   env("OWO_SESSION_SECRET", "dev-only-insecure-secret"),
		PortRangeStart:  envInt("OWO_PORT_RANGE_START", 25565),
		PortRangeEnd:    envInt("OWO_PORT_RANGE_END", 25665),
		MaxServers:      envInt("OWO_MAX_SERVERS", 10),
		ProbeHostPorts:  envBool("OWO_PROBE_HOST_PORTS", true),
		DefaultCPULimit: envFloat("OWO_DEFAULT_CPU_LIMIT", 2.0),
		IdleTimeout:     envDuration("OWO_IDLE_TIMEOUT", 30*time.Minute),

		SecureCookies:     envBool("OWO_SECURE_COOKIES", false),
		AllowRegistration: envBool("OWO_ALLOW_REGISTRATION", true),
		AdminUsername:     env("OWO_ADMIN_USERNAME", ""),
		AdminPassword:     env("OWO_ADMIN_PASSWORD", ""),
		CurseForgeAPIKey:  env("OWO_CURSEFORGE_API_KEY", ""),
		CORSOrigin:        env("OWO_CORS_ORIGIN", "http://localhost:5173"),
		StopTimeout:       envDuration("OWO_STOP_TIMEOUT", 60*time.Second),
		PublicHostname:    env("OWO_PUBLIC_HOSTNAME", "localhost"),
	}

	if c.PortRangeStart < 1 || c.PortRangeStart > 65535 {
		return nil, fmt.Errorf("OWO_PORT_RANGE_START %d out of range", c.PortRangeStart)
	}
	if c.PortRangeEnd < c.PortRangeStart || c.PortRangeEnd > 65535 {
		return nil, fmt.Errorf("OWO_PORT_RANGE_END %d must be >= start and <= 65535", c.PortRangeEnd)
	}
	if c.MaxServers < 1 {
		return nil, fmt.Errorf("OWO_MAX_SERVERS must be at least 1")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
