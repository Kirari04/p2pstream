package cmd

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/logger"
	"p2pstream/internal/server"
)

const (
	serverShutdownTimeout             = 5 * time.Second
	observabilityRecorderCloseTimeout = 5 * time.Second
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the p2pstream proxy server",
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.Load()
		if err != nil {
			os.Stderr.WriteString("Failed to load config: " + err.Error() + "\n")
			os.Exit(1)
		}

		logger.Init(cfg.Env)

		database, err := db.Open(cfg.DatabaseURL)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to initialize database")
		}
		defer database.Close()

		app := server.NewApp(cfg, database)
		defer app.CloseAgentTransports()
		defer func() {
			if err := app.ClosePublicGeoRuntime(); err != nil {
				log.Warn().Err(err).Msg("Failed to close GeoIP runtime")
			}
		}()
		if err := app.LoadPublicGeoRuntime(); err != nil && !os.IsNotExist(err) {
			log.Warn().Err(err).Msg("Failed to load the existing GeoIP country database")
		}

		// Setup Management Server
		mgmtMux := http.NewServeMux()
		app.RegisterManagementRoutes(mgmtMux)
		mgmtHandler := app.ManagementClientIdentityMiddleware(server.ManagementClientCertificateMiddleware(mgmtMux))

		mgmtTLSConfig, managementTLS, err := server.NewManagementTLSConfig(cfg)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to initialize management TLS")
		}

		p := new(http.Protocols)
		p.SetHTTP1(true)
		if managementTLS {
			p.SetHTTP2(true)
		} else {
			// Setup h2c for ConnectRPC to support HTTP/2 without TLS in dev mode.
			p.SetUnencryptedHTTP2(true)
		}

		mgmtAddr := managementListenAddress(cfg)
		mgmtSrv := &http.Server{
			Addr:      mgmtAddr,
			Handler:   mgmtHandler,
			Protocols: p,
			TLSConfig: mgmtTLSConfig,
		}
		server.ConfigureManagementHTTPServer(mgmtSrv)

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		app.StartObservabilityMaintenance(ctx)
		app.StartPublicGeoMaintenance(ctx)

		if app.PublicACME != nil {
			app.PublicACME.Start(ctx)
		}

		if _, err := app.StartProxyListener(context.Background()); err != nil {
			log.Error().Err(err).Msg("Proxy server failed to start")
		}
		app.StartDashboardCache(ctx)

		// Start Management Listener
		go func() {
			scheme := "http"
			if managementTLS {
				scheme = "https"
			}
			displayAddr := managementDisplayAddress(mgmtAddr)
			managementURL := scheme + "://" + displayAddr
			app.LogGeneratedSetupToken(managementURL)
			log.Info().
				Str("url", managementURL).
				Str("bind", mgmtAddr).
				Str("agent_tunnel", managementURL+"/agent/tunnel").
				Msg("Management server listening")
			var err error
			if managementTLS {
				err = mgmtSrv.ListenAndServeTLS("", "")
			} else {
				err = mgmtSrv.ListenAndServe()
			}
			if err != nil && err != http.ErrServerClosed {
				log.Fatal().Err(err).Msg("Management server failed to start - application cannot continue")
			}
		}()

		// Wait for interruption
		<-ctx.Done()
		log.Info().Msg("Shutting down servers gracefully...")

		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), serverShutdownTimeout)

		var shutdownErrs []error
		if _, err := app.StopProxyListener(shutdownCtx); err != nil {
			shutdownErrs = append(shutdownErrs, err)
		}
		if err := mgmtSrv.Shutdown(shutdownCtx); err != nil {
			shutdownErrs = append(shutdownErrs, err)
		}
		cancelShutdown()

		// Listener shutdown can consume its entire shared deadline. Give the
		// recorder an independent bounded chance to persist already-queued data.
		recorderCloseCtx, cancelRecorderClose := context.WithTimeout(context.Background(), observabilityRecorderCloseTimeout)
		if err := app.CloseObservabilityRecorder(recorderCloseCtx); err != nil {
			shutdownErrs = append(shutdownErrs, err)
		}
		cancelRecorderClose()

		if len(shutdownErrs) > 0 {
			log.Error().Errs("errors", shutdownErrs).Msg("Errors during shutdown")
		}

		log.Info().Msg("Servers stopped cleanly")
	},
}

func managementListenAddress(cfg *config.Config) string {
	if cfg == nil {
		cfg = &config.Config{}
	}
	bind := cfg.ManagementBindAddress
	if bind == "" {
		bind = "0.0.0.0"
	}
	port := cfg.ManagementPort
	if port == "" {
		port = "8081"
	}
	return net.JoinHostPort(bind, port)
}

func managementDisplayAddress(listenAddr string) string {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil || port == "" {
		port = "8081"
	}
	return net.JoinHostPort("localhost", port)
}

func init() {
	rootCmd.AddCommand(serverCmd)
}
