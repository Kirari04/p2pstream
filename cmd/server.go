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

		mux := http.NewServeMux()
		app.RegisterRoutes(mux)

		addr := ":" + cfg.Port
		srv := &http.Server{
			Addr:    addr,
			Handler: mux,
		}

		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

		go func() {
			log.Info().
				Str("url", "http://localhost"+addr).
				Str("target", cfg.TargetOrigin).
				Str("ws", "ws://localhost"+addr+"/ws").
				Msg("Proxy server started")

			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatal().Err(err).Msg("Server failed")
			}
		}()

		<-stop
		log.Info().Msg("Shutting down server gracefully...")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			log.Error().Err(err).Msg("Server forced to shutdown")
		}

		log.Info().Msg("Server stopped cleanly")
	},
}

func init() {
	rootCmd.AddCommand(serverCmd)
}
