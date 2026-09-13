package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/releaseversion"
	"p2pstream/internal/serverupdate"
)

func init() {
	command := &cobra.Command{Use: "server-updater", Short: "Operate the independently supervised server updater"}
	var configPath string
	serve := &cobra.Command{Use: "serve", Short: "Serve the private update socket and recover interrupted deployments", RunE: func(cmd *cobra.Command, args []string) error { return serveServerUpdater(configPath, false) }}
	serve.Flags().StringVar(&configPath, "config", "/etc/p2pstream-server-updater/config.json", "locally installed executor configuration")
	var directory, model, image, updater string
	enroll := &cobra.Command{Use: "enroll", Short: "Adopt a trusted, resolved Compose model (local installation only)", RunE: func(cmd *cobra.Command, args []string) error {
		data, err := os.ReadFile(model)
		if err != nil {
			return err
		}
		channel := "stable"
		if releaseversion.Prerelease(buildinfo.Version) {
			channel = "staging"
		}
		source, err := serverupdate.NewGitHubSource(buildinfo.RepositorySlug(), channel, runtime.GOARCH)
		if err != nil {
			return err
		}
		release, err := source.Installed(cmd.Context(), image, serverupdate.RuntimeStatus{API: serverupdate.API, Schema: serverupdate.Schema, Version: buildinfo.Version, Commit: buildinfo.Commit})
		if err != nil {
			return err
		}
		return serverupdate.Enroll(directory, data, image, updater, buildinfo.Version, buildinfo.RepositorySlug(), release.VerificationFloor())
	}}
	enroll.Flags().StringVar(&directory, "directory", "/etc/p2pstream-server-updater", "private enrollment directory")
	enroll.Flags().StringVar(&model, "compose", "", "resolved Compose JSON")
	enroll.Flags().StringVar(&image, "image", "", "installed release digest")
	enroll.Flags().StringVar(&updater, "updater-image", "", "locally built pinned updater image ID")
	recover := &cobra.Command{Use: "recover", Short: "Retry paused recovery once (stop the updater service first)", RunE: func(cmd *cobra.Command, args []string) error { return serveServerUpdater(configPath, true) }}
	recover.Flags().StringVar(&configPath, "config", "/etc/p2pstream-server-updater/config.json", "locally installed executor configuration")
	command.AddCommand(serve, enroll, recover)
	rootCmd.AddCommand(command)
	rootCmd.AddCommand(&cobra.Command{Use: "server-update-status [status|prepare]", Short: "Query the container-local server readiness socket", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] != "status" && args[0] != "prepare" {
			return errors.New("expected status or prepare")
		}
		t := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", serverupdate.RuntimeSocket)
		}}
		defer t.CloseIdleConnections()
		client := &http.Client{Transport: t, Timeout: 45 * time.Second}
		r, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, "http://server/"+args[0], nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(r)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return errors.New("server readiness unavailable")
		}
		_, err = io.Copy(cmd.OutOrStdout(), io.LimitReader(resp.Body, 64<<10))
		return err
	}})
}

func serveServerUpdater(configPath string, retry bool) error {
	if os.Geteuid() != 0 {
		return errors.New("the Docker updater must run as root in its dedicated container")
	}
	info, err := os.Lstat(configPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("executor configuration must be a private regular file")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var config serverupdate.ComposeConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}
	if err := config.Validate(); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(config.StateDir, "executor.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another executor owns this deployment")
	}
	source, err := serverupdate.NewGitHubSource(config.Repository, config.Channel, runtime.GOARCH)
	if err != nil {
		return err
	}
	engine, err := serverupdate.NewEngine(config.StateDir, config.InstanceID, config.Channel, &serverupdate.ComposeDriver{Config: config}, source, config.BootstrapFloor)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := engine.Recover(ctx, retry); err != nil {
		return err
	}
	if retry {
		return nil
	}
	socket := filepath.Join(config.ControlDir, "control.sock")
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("unsafe updater socket path")
		}
		if err := os.Remove(socket); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socket)
	// The socket lives in a private bind shared with the enrolled server. A
	// separate random token authenticates requests from its non-root UID.
	if err := os.Chmod(socket, 0666); err != nil {
		return err
	}
	srv := &http.Server{Handler: engine.Handler(config.Token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 90 * time.Second}
	errs := make(chan error, 2)
	go func() { errs <- engine.Run(ctx) }()
	go func() { errs <- srv.Serve(listener) }()
	select {
	case <-ctx.Done():
	case err := <-errs:
		cancel()
		_ = srv.Close()
		return fmt.Errorf("server updater stopped: %w", err)
	}
	_ = srv.Close()
	return nil
}
