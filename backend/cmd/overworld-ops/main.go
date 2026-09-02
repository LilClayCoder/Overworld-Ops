// Command overworld-ops runs the Minecraft server control plane: a REST API
// over the Docker Engine that creates, starts, stops and deletes per-server
// itzg/minecraft-server containers.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/calebgrabowski/overworld-ops/backend/internal/api"
	"github.com/calebgrabowski/overworld-ops/backend/internal/config"
	"github.com/calebgrabowski/overworld-ops/backend/internal/docker"
	"github.com/calebgrabowski/overworld-ops/backend/internal/ports"
	"github.com/calebgrabowski/overworld-ops/backend/internal/store"
)

func main() {
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// Cancelled on SIGINT/SIGTERM so shutdown is orderly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if cfg.SessionSecret == "dev-only-insecure-secret" {
		slog.Warn("OWO_SESSION_SECRET is unset; sessions are signed with a well-known key")
	}

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	slog.Info("database ready", "path", cfg.DatabaseURL)

	if err := api.SeedAdmin(ctx, st, cfg.AdminUsername, cfg.AdminPassword); err != nil {
		return err
	}

	dk, err := docker.New(cfg.DockerHost, cfg.MinecraftImage)
	if err != nil {
		return err
	}
	defer dk.Close()

	if err := dk.Ping(ctx); err != nil {
		return err
	}
	slog.Info("docker engine reachable")

	// Pull the image up front. The first pull is several hundred megabytes,
	// and doing it here rather than inside a create request keeps the create
	// call fast and its failures meaningful.
	slog.Info("ensuring minecraft image is present", "image", cfg.MinecraftImage)
	if err := dk.EnsureImage(ctx); err != nil {
		// Not fatal: the daemon may be offline from the registry while still
		// holding a usable cached image, and create will report the real
		// problem if it is genuinely missing.
		slog.Warn("could not pull minecraft image", "error", err)
	}

	alloc := ports.New(cfg.PortRangeStart, cfg.PortRangeEnd, st.UsedPorts, cfg.ProbeHostPorts)

	srv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: api.New(cfg, st, dk, alloc).Routes(),
		// No WriteTimeout: the log endpoint streams indefinitely and a write
		// deadline would cut healthy consoles off mid-tail.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("api listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	// Give in-flight requests a moment; game containers keep running, which is
	// intentional — restarting the control plane must not disconnect players.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
