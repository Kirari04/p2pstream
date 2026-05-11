package cmd

import (
	"context"
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

		// Setup Management Server
		mgmtMux := http.NewServeMux()
		app.RegisterManagementRoutes(mgmtMux)
		mgmtHandler := server.ManagementClientCertificateMiddleware(mgmtMux)

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

		mgmtAddr := ":" + cfg.ManagementPort
		mgmtSrv := &http.Server{
			Addr:      mgmtAddr,
			Handler:   mgmtHandler,
			Protocols: p,
			TLSConfig: mgmtTLSConfig,
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		if _, err := app.StartProxyListener(context.Background()); err != nil {
			log.Error().Err(err).Msg("Proxy server failed to start")
		}

		// Start Management Listener
		go func() {
			scheme := "http"
			wsScheme := "ws"
			if managementTLS {
				scheme = "https"
				wsScheme = "wss"
			}
			log.Info().
				Str("url", scheme+"://localhost"+mgmtAddr).
				Str("ws", wsScheme+"://localhost"+mgmtAddr+"/ws").
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

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var shutdownErrs []error
		if _, err := app.StopProxyListener(shutdownCtx); err != nil {
			shutdownErrs = append(shutdownErrs, err)
		}
		if err := mgmtSrv.Shutdown(shutdownCtx); err != nil {
			shutdownErrs = append(shutdownErrs, err)
		}

		if len(shutdownErrs) > 0 {
			log.Error().Errs("errors", shutdownErrs).Msg("Errors during shutdown")
		}

		log.Info().Msg("Servers stopped cleanly")
	},
}

func init() {
	rootCmd.AddCommand(serverCmd)
}
