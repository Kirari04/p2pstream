package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"

	"p2pstream/internal/db/migrations"
)

func runEmbeddedMigrations(database *sql.DB) error {
	provider, err := goose.NewProvider(goose.DialectSQLite3, database, migrations.FS)
	if err != nil {
		return fmt.Errorf("configure db migrations: %w", err)
	}
	if _, err := provider.Up(context.Background()); err != nil {
		return fmt.Errorf("run db migrations: %w", err)
	}
	return nil
}
