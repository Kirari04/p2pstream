package db

import (
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"

	"p2pstream/internal/db/migrations"
)

func runEmbeddedMigrations(database *sql.DB) error {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("configure db migrations: %w", err)
	}
	if err := goose.Up(database, "."); err != nil {
		return fmt.Errorf("run db migrations: %w", err)
	}
	return nil
}
