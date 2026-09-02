package docker

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/go-connections/nat"
)

// gamePort is the port the Minecraft server listens on inside the container.
// It is always 25565; only the host-side port varies per server.
const gamePort = "25565/tcp"

// Spec describes a Minecraft server container to create. It is deliberately
// independent of the database model so this package stays testable on its own.
type Spec struct {
	// ServerID and OwnerID are stamped as labels for reconciliation.
	ServerID string
	OwnerID  string

	// ContainerName and VolumeName must be stable for the life of the server.
	ContainerName string
	VolumeName    string

	// Type is the itzg TYPE value: VANILLA, FORGE, FABRIC, NEOFORGE,
	// MODRINTH or AUTO_CURSEFORGE.
	Type string
	// Version is the Minecraft version, e.g. "1.21.1" or "LATEST".
	Version string

	// HostPort is the host-side port forwarded to the container's 25565.
	HostPort int

	// Memory is the JVM heap, e.g. "4G". MemoryLimitBytes caps the whole
	// container; leave it zero to derive headroom from Memory automatically.
	Memory           string
	MemoryLimitBytes int64

	// CPULimit is a fractional core count, e.g. 2.5. Zero means uncapped.
	CPULimit float64

	// ModrinthModpack is a Modrinth project slug, version ID or .mrpack URL.
	ModrinthModpack string
	// CurseForgeSlug is a CurseForge modpack slug; CurseForgeAPIKey is
	// required alongside it because CurseForge gates pack downloads.
	CurseForgeSlug   string
	CurseForgeAPIKey string

	// ExtraEnv is merged last, so it can override any computed value. Useful
	// for one-off tuning (DIFFICULTY, MOTD, OPS, ...) without a code change.
	ExtraEnv map[string]string
}

// State is the coarse container state the API reports back to the UI.
type State struct {
	ContainerID string
	// Status is the raw Docker status: created, running, paused, restarting,
	// removing, exited or dead.
	Status  string
	Running bool
	// Health is the container healthcheck result, if the image defines one.
	// The itzg image reports "starting" while the world generates and
	// "healthy" once the server answers pings — which is what the UI wants.
	Health   string
	ExitCode int
	Error    string
}

// CreateServer creates the volume and container for a Minecraft server. It
// does not start it; call StartServer next. Returns the new container ID.
func (c *Client) CreateServer(ctx context.Context, spec Spec) (string, error) {
	// A named volume keeps world data alive across container recreation, so a
	// version bump or memory change never eats somebody's base.
	_, err := c.api.VolumeCreate(ctx, volume.CreateOptions{
		Name: spec.VolumeName,
		Labels: map[string]string{
			LabelManaged:  "true",
			LabelServerID: spec.ServerID,
			LabelOwnerID:  spec.OwnerID,
		},
	})
	if err != nil {
		return "", fmt.Errorf("create volume %s: %w", spec.VolumeName, err)
	}

	hostBinding := nat.PortBinding{
		HostIP:   "0.0.0.0",
		HostPort: strconv.Itoa(spec.HostPort),
	}

	cfg := &container.Config{
		Image: c.image,
		Env:   buildEnv(spec),
		Labels: map[string]string{
			LabelManaged:  "true",
			LabelServerID: spec.ServerID,
			LabelOwnerID:  spec.OwnerID,
		},
		ExposedPorts: nat.PortSet{nat.Port(gamePort): struct{}{}},
		// The itzg image reads console commands from stdin, which is how the
		// console feature will send /say, /op and friends later.
		OpenStdin: true,
		Tty:       false,
	}

	hostCfg := &container.HostConfig{
		PortBindings: nat.PortMap{nat.Port(gamePort): []nat.PortBinding{hostBinding}},
		Mounts: []mount.Mount{{
			Type:   mount.TypeVolume,
			Source: spec.VolumeName,
			Target: "/data",
		}},
		// unless-stopped brings servers back after a host reboot but respects
		// an explicit stop from the dashboard.
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		Resources:     buildResources(spec),
	}

	resp, err := c.api.ContainerCreate(ctx, cfg, hostCfg, nil, nil, spec.ContainerName)
	if err != nil {
		return "", fmt.Errorf("create container %s: %w", spec.ContainerName, err)
	}
	return resp.ID, nil
}

// StartServer starts an existing container.
func (c *Client) StartServer(ctx context.Context, containerID string) error {
	if err := c.api.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		if isNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("start container %s: %w", containerID, err)
	}
	return nil
}

// StopServer stops a container, giving the JVM timeoutSeconds to flush chunks
// and save the world before it is killed. Minecraft needs a real grace period:
// a large modded world can take 30+ seconds to save.
func (c *Client) StopServer(ctx context.Context, containerID string, timeoutSeconds int) error {
	err := c.api.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeoutSeconds})
	if err != nil {
		if isNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("stop container %s: %w", containerID, err)
	}
	return nil
}

// RemoveServer deletes the container. When removeVolume is true the world data
// volume is deleted too — that is irreversible, so the caller must have
// confirmed it with the user.
func (c *Client) RemoveServer(ctx context.Context, containerID, volumeName string, removeVolume bool) error {
	err := c.api.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("remove container %s: %w", containerID, err)
	}

	if removeVolume && volumeName != "" {
		if err := c.api.VolumeRemove(ctx, volumeName, false); err != nil && !isNotFound(err) {
			return fmt.Errorf("remove volume %s: %w", volumeName, err)
		}
	}
	return nil
}

// InspectServer reports the current container state.
func (c *Client) InspectServer(ctx context.Context, containerID string) (*State, error) {
	info, err := c.api.ContainerInspect(ctx, containerID)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("inspect container %s: %w", containerID, err)
	}

	st := &State{ContainerID: info.ID}
	if info.State != nil {
		st.Status = string(info.State.Status)
		st.Running = info.State.Running
		st.ExitCode = info.State.ExitCode
		st.Error = info.State.Error
		if info.State.Health != nil {
			st.Health = info.State.Health.Status
		}
	}
	return st, nil
}

// FindByServerID locates the container for a server by its label, without
// needing a stored container ID. This is how startup reconciliation reattaches
// to containers after the database has been restored from a backup.
func (c *Client) FindByServerID(ctx context.Context, serverID string) (string, error) {
	f := filters.NewArgs(
		filters.Arg("label", LabelManaged+"=true"),
		filters.Arg("label", LabelServerID+"="+serverID),
	)
	list, err := c.api.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return "", fmt.Errorf("list containers: %w", err)
	}
	if len(list) == 0 {
		return "", ErrNotFound
	}
	return list[0].ID, nil
}

// Logs returns a stream of container logs. When follow is true the stream
// stays open and the caller must close it (or cancel ctx) to stop tailing.
// tail is the number of historical lines to replay, e.g. "200" or "all".
//
// Note: because the container is created without a TTY, the returned stream is
// Docker's multiplexed stdout/stderr format. Use DemuxLogs to read it.
func (c *Client) Logs(ctx context.Context, containerID string, follow bool, tail string) (io.ReadCloser, error) {
	if tail == "" {
		tail = "200"
	}
	rc, err := c.api.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("container logs %s: %w", containerID, err)
	}
	return rc, nil
}

// buildEnv assembles the itzg/minecraft-server environment. The image reads
// everything from env vars, which is exactly why it is a good fit here: no
// config file templating, no server.properties editing.
func buildEnv(spec Spec) []string {
	env := map[string]string{
		// The image refuses to boot without this. Accepting it is the user's
		// act of creating a server through the UI, which says so on the form.
		"EULA":    "TRUE",
		"TYPE":    spec.Type,
		"VERSION": spec.Version,
		"MEMORY":  spec.Memory,
		// Surface a stable server name in the multiplayer list.
		"ENABLE_RCON": "false",
	}

	// A modpack overrides TYPE: itzg has dedicated download paths for each
	// provider and picks the right loader from the pack manifest itself.
	switch {
	case spec.ModrinthModpack != "":
		env["TYPE"] = "MODRINTH"
		env["MODRINTH_MODPACK"] = spec.ModrinthModpack
		// The pack pins its own Minecraft version; leaving VERSION set would
		// fight with it.
		delete(env, "VERSION")
	case spec.CurseForgeSlug != "":
		env["TYPE"] = "AUTO_CURSEFORGE"
		env["CF_SLUG"] = spec.CurseForgeSlug
		env["CF_API_KEY"] = spec.CurseForgeAPIKey
		delete(env, "VERSION")
	}

	for k, v := range spec.ExtraEnv {
		env[k] = v
	}

	out := make([]string, 0, len(env))
	for k, v := range env {
		if v == "" {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

// buildResources translates the spec's caps into container limits.
func buildResources(spec Spec) container.Resources {
	res := container.Resources{}

	if spec.CPULimit > 0 {
		// NanoCPUs is billionths of a core: 1.5 cores -> 1_500_000_000.
		res.NanoCPUs = int64(spec.CPULimit * 1e9)
	}

	limit := spec.MemoryLimitBytes
	if limit == 0 {
		limit = deriveMemoryLimit(spec.Memory)
	}
	res.Memory = limit
	return res
}

// deriveMemoryLimit turns a JVM heap size into a container memory cap. The
// heap is not the whole story — metaspace, the JIT code cache, GC structures
// and off-heap buffers all live outside it — so the container gets the heap
// plus 1 GiB of headroom. Without that margin the kernel OOM-kills the JVM
// mid-chunk-save, which corrupts worlds.
func deriveMemoryLimit(heap string) int64 {
	bytes, err := parseSize(heap)
	if err != nil || bytes == 0 {
		return 0 // uncapped rather than wrongly capped
	}
	return bytes + (1 << 30)
}

// parseSize parses JVM-style sizes: "2G", "512M", "1024K" or plain bytes.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}

	mult := int64(1)
	switch s[len(s)-1] {
	case 'G':
		mult, s = 1<<30, s[:len(s)-1]
	case 'M':
		mult, s = 1<<20, s[:len(s)-1]
	case 'K':
		mult, s = 1<<10, s[:len(s)-1]
	}

	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse size %q: %w", s, err)
	}
	return n * mult, nil
}
