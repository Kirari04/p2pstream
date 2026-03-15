package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Init initializes the global zerolog logger based on the environment.
// In production, it uses JSON format. In development, it uses a colored text console writer.
func Init(env string) {
	zerolog.TimeFieldFormat = time.RFC3339

	if env == "production" {
		// Default is JSON, just set global level
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	} else {
		// Development: human readable console output
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}
}
