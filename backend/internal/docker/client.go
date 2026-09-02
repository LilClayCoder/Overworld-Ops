// Package docker wraps the Docker Engine API with the narrow set of
// operations this platform needs: create, start, stop, remove and tail the
// logs of a Minecraft server container.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// Label keys stamped onto every container this platform creates. They let us
// find our own containers again after a restart and ignore everything else
// running on the host.
const (
	LabelManaged  = "overworld-ops.managed"
	LabelServerID = "overworld-ops.server-id"
	LabelOwnerID  = "overworld-ops.owner-id"
)

// ErrNotFound reports that a container or volume does not exist.
var ErrNotFound = errors.New("docker: resource not found")

// Client is a thin wrapper over the Docker Engine API client.
type Client struct {
	api *client.Client
	// image is the Minecraft server image every container runs.
	image string
}

// New connects to the Docker Engine. host is optional: empty means read
// DOCKER_HOST from the environment, falling back to the local socket
// (/var/run/docker.sock) or named pipe on Windows.
func New(host, mcImage string) (*Client, error) {
	opts := []client.Opt{
		client.FromEnv,
		// Negotiate down to whatever API version the daemon speaks, so the
		// same binary works against an older Engine.
		client.WithAPIVersionNegotiation(),
	}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}

	api, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &Client{api: api, image: mcImage}, nil
}

// Ping verifies the daemon is reachable. Call it at startup so a
// misconfigured DOCKER_HOST fails loudly instead of on the first create.
func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.api.Ping(ctx); err != nil {
		return fmt.Errorf("docker daemon unreachable: %w", err)
	}
	return nil
}

// Close releases the underlying HTTP client.
func (c *Client) Close() error { return c.api.Close() }

// Image returns the configured Minecraft server image reference.
func (c *Client) Image() string { return c.image }

// EnsureImage pulls the Minecraft server image if it is not present locally.
// The first pull of itzg/minecraft-server is a few hundred megabytes, so this
// is worth doing once at startup rather than inside a create request.
func (c *Client) EnsureImage(ctx context.Context) error {
	if _, err := c.api.ImageInspect(ctx, c.image); err == nil {
		return nil // already local
	}

	rc, err := c.api.ImagePull(ctx, c.image, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", c.image, err)
	}
	defer rc.Close()

	// The pull only actually happens while the response body is being read;
	// decode and discard the progress stream, surfacing any error frame.
	dec := json.NewDecoder(rc)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read pull progress: %w", err)
		}
		if msg.Error != "" {
			return fmt.Errorf("pull image %s: %s", c.image, msg.Error)
		}
	}
}

// isNotFound maps the Engine's 404 responses onto ErrNotFound.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return client.IsErrNotFound(err) || strings.Contains(err.Error(), "No such container")
}
