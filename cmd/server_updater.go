package cmd

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"p2pstream/internal/config"
	"p2pstream/internal/server"
	"path/filepath"
	"runtime"
	"strings"
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
	var directory, deploymentDirectory, model, image, updater, installation, releaseDirectory string
	enroll := &cobra.Command{Use: "enroll", Short: "Adopt a trusted, resolved Compose model (local installation only)", RunE: func(cmd *cobra.Command, args []string) error {
		data, err := os.ReadFile(model)
		if err != nil {
			return err
		}
		channel := "stable"
		if releaseversion.Prerelease(buildinfo.Version) {
			channel = "staging"
		}
		var floor serverupdate.Floor
		current := serverupdate.RuntimeStatus{API: serverupdate.API, Schema: serverupdate.Schema, Version: buildinfo.Version, Commit: buildinfo.Commit}
		if releaseDirectory != "" {
			floor, err = serverupdate.VerifyEnrollmentInputs(releaseDirectory, buildinfo.RepositorySlug(), channel, runtime.GOARCH, image, updater, current)
		} else {
			var source *serverupdate.GitHubSource
			source, err = serverupdate.NewGitHubSource(buildinfo.RepositorySlug(), channel, runtime.GOARCH)
			if err == nil {
				var release serverupdate.Release
				release, err = source.Installed(cmd.Context(), image, current)
				floor = release.VerificationFloor()
			}
		}
		if err != nil {
			return err
		}
		if installation == "" {
			return errors.New("--installation is required; read it from the running server before enrollment")
		}
		if deploymentDirectory != "" {
			return serverupdate.EnrollManagedInstallation(directory, deploymentDirectory, data, image, updater, buildinfo.Version, buildinfo.RepositorySlug(), floor, installation)
		}
		return serverupdate.EnrollInstallation(directory, data, image, updater, buildinfo.Version, buildinfo.RepositorySlug(), floor, installation)
	}}
	enroll.Flags().StringVar(&directory, "directory", "/etc/p2pstream-server-updater", "private enrollment directory")
	enroll.Flags().StringVar(&deploymentDirectory, "deployment-directory", "", "root-owned group-readable canonical deployment directory (protocol 2)")
	enroll.Flags().StringVar(&model, "compose", "", "resolved Compose JSON")
	enroll.Flags().StringVar(&image, "image", "", "installed release digest")
	enroll.Flags().StringVar(&releaseDirectory, "release-directory", "", "protected verified release assets for offline enrollment")
	enroll.Flags().StringVar(&installation, "installation", "", "pre-enrollment running installation identity")
	enroll.Flags().StringVar(&updater, "updater-image", "", "published pinned updater platform digest")
	recover := &cobra.Command{Use: "recover", Short: "Retry paused recovery once (stop the updater service first)", RunE: func(cmd *cobra.Command, args []string) error { return serveServerUpdater(configPath, true) }}
	recover.Flags().StringVar(&configPath, "config", "/etc/p2pstream-server-updater/config.json", "locally installed executor configuration")
	var candidate, output string
	configure := &cobra.Command{Use: "configure", Short: "Validate an edited deployment for the trusted host controller", RunE: func(cmd *cobra.Command, args []string) error {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return err
		}
		var cfg serverupdate.ComposeConfig
		if err = json.Unmarshal(data, &cfg); err != nil {
			return err
		}
		input, err := os.ReadFile(candidate)
		if err != nil {
			return err
		}
		current, err := os.ReadFile(cfg.DeploymentFile)
		if err != nil {
			return err
		}
		result, err := serverupdate.ConfigureModel(cfg, input, current)
		if err != nil {
			return err
		}
		return serverupdate.AtomicWrite(output, result, 0600)
	}}
	configure.Flags().StringVar(&configPath, "config", "/etc/p2pstream-server-updater/config.json", "executor configuration")
	configure.Flags().StringVar(&candidate, "compose", "", "resolved candidate model")
	configure.Flags().StringVar(&output, "output", "", "validated candidate model")
	check := &cobra.Command{Use: "check", Short: "Verify the authenticated executor and managed server", RunE: func(cmd *cobra.Command, args []string) error {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return err
		}
		var cfg serverupdate.ComposeConfig
		if err = json.Unmarshal(data, &cfg); err != nil {
			return err
		}
		if err = cfg.Validate(); err != nil {
			return err
		}
		r, err := serverupdate.Call(cmd.Context(), filepath.Join(cfg.ControlDir, "control.sock"), cfg.Token, "/status", serverupdate.Request{})
		if err != nil {
			return err
		}
		if r.Overview == nil || r.Overview.InstanceID != cfg.InstanceID {
			return errors.New("updater identity mismatch")
		}
		status, _, _, err := (&serverupdate.ComposeDriver{Config: cfg}).Inspect(cmd.Context())
		if err != nil {
			return err
		}
		if err := validateServerUpdaterStatus(status, cfg.InstanceID); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	}}
	check.Flags().StringVar(&configPath, "config", "/etc/p2pstream-server-updater/config.json", "executor configuration")
	for _, action := range []string{"runtime-block", "runtime-allow", "runtime-detach", "record-runtime"} {
		var path string
		guard := &cobra.Command{Use: action, Short: "Coordinate deployment ownership (trusted host controller only)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() != 0 {
				return errors.New("deployment coordination must run as root")
			}
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				return errors.New("executor configuration must be a private regular file")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var cfg serverupdate.ComposeConfig
			if err := json.Unmarshal(data, &cfg); err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			driver := &serverupdate.ComposeDriver{Config: cfg}
			switch action {
			case "runtime-block":
				return driver.BlockRuntime(cmd.Context())
			case "runtime-allow":
				return driver.AllowRuntime(cmd.Context())
			case "runtime-detach":
				return driver.DetachRuntime(cmd.Context())
			default:
				return driver.RecordCurrentRuntimeReceipt(cmd.Context())
			}
		}}
		guard.Flags().StringVar(&path, "config", "/etc/p2pstream-server-updater/config.json", "executor configuration")
		command.AddCommand(guard)
	}
	rootCmd.AddCommand(&cobra.Command{Use: "server-deployment-capabilities", Short: "Report immutable deployment capabilities without opening application data", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(serverupdate.DeploymentCapabilities{RuntimeGuard: 2, Version: buildinfo.Version, Commit: buildinfo.Commit})
	}})
	command.AddCommand(serve, enroll, recover, configure, check)
	rootCmd.AddCommand(&cobra.Command{Use: "server-installation-identity", Short: "Read the existing data-backed installation identity", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		dir := os.Getenv("CONFIG_DIR")
		if dir == "" {
			dir = "p2pstream-data"
		}
		id, err := serverupdate.InstallationIdentity(dir, false)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), id)
		return nil
	}})
	rootCmd.AddCommand(&cobra.Command{Use: "server-health", Short: "Verify the local management listener using its persisted certificate", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadReadOnly()
		if err != nil {
			return err
		}
		serverTLS, enabled, err := server.ReadManagementTLSConfig(cfg)
		if err != nil {
			return err
		}
		scheme := "http"
		transport := &http.Transport{Proxy: nil}
		if enabled {
			scheme = "https"
			pool := x509.NewCertPool()
			cert, err := x509.ParseCertificate(serverTLS.Certificates[0].Certificate[0])
			if err != nil {
				return err
			}
			pool.AddCert(cert)
			u, err := url.Parse(cfg.ManagementPublicURL)
			if err != nil {
				return err
			}
			name := u.Hostname()
			if name == "" {
				name = "localhost"
			}
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: name}
		}
		defer transport.CloseIdleConnections()
		host := cfg.ManagementBindAddress
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
		req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, scheme+"://"+net.JoinHostPort(host, cfg.ManagementPort)+"/p2pstream.v1.AgentManagementService/GetSetupState", bytes.NewBufferString("{}"))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return errors.New("local management listener is unhealthy")
		}
		return nil
	}})
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

func validateServerUpdaterStatus(status serverupdate.RuntimeStatus, instanceID string) error {
	var reasons []string
	if !status.Ready {
		reasons = append(reasons, "readiness is false")
	}
	if status.InstanceID != instanceID {
		reasons = append(reasons, "server instance mismatch")
	}
	if status.Maintenance {
		reasons = append(reasons, "maintenance is active")
	}
	// Only emit the fixed messages from serverUpdateRuntimeStatus. Never echo
	// arbitrary runtime text or identity values into host-controller diagnostics.
	// Deduplication bounds the output even if a runtime repeats blockers.
	seen := make(map[string]bool)
	unknown := false
	for _, reason := range status.Blocked {
		switch reason {
		case "Database schema is unavailable",
			"Finish or cancel active agent updates before updating the server",
			"Finish management certificate rotation before updating the server",
			"An enabled public listener is not running",
			"Automatic backups require the standard /data database; use a manual deployment for custom database paths",
			"Custom state paths outside /data require a manual deployment":
			if !seen[reason] {
				reasons = append(reasons, "blocked: "+reason)
				seen[reason] = true
			}
		default:
			unknown = true
		}
	}
	if unknown {
		reasons = append(reasons, "blocked: unrecognized runtime blocker (details omitted)")
	}
	if len(reasons) == 0 {
		return nil
	}
	return fmt.Errorf("server is not healthy: %s", strings.Join(reasons, "; "))
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
