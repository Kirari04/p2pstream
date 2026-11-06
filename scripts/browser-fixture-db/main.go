// Prepare only disposable browser databases, before the real server starts.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"p2pstream/internal/db"
)

func prepare(path string) error {
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return fmt.Errorf("browser fixture requires a new database")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	database, err := db.Open(path)
	if err != nil {
		return err
	}
	defer database.Close()
	// Keep the production welcome Site/certificate seed, but prevent its default
	// wildcard :80/:443 listeners from ever opening in a browser fixture. Tests
	// explicitly create or enable the loopback listeners that they need later.
	_, err = database.Exec(`CREATE TRIGGER browser_fixture_seed_listeners
AFTER INSERT ON public_listeners
WHEN NEW.name IN ('public-http', 'public-https')
BEGIN
  UPDATE public_listeners SET bind_address = '127.0.0.1', enabled = 0 WHERE id = NEW.id;
END`)
	return err
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: browser-fixture-db DATABASE_PATH")
		os.Exit(1)
	}
	if err := prepare(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "prepare browser fixture:", err)
		os.Exit(1)
	}
}
