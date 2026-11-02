package main

import (
	"path/filepath"
	"testing"

	"p2pstream/internal/db"
)

func TestFixtureSeedsCannotOpenWildcardListeners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "fixture.db")
	if err := prepare(path); err != nil {
		t.Fatal(err)
	}
	if err := prepare(path); err == nil {
		t.Fatal("accepted an existing database")
	}
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, listener := range []struct {
		name, protocol string
		port           int
	}{{"public-http", "http", 80}, {"public-https", "https", 443}} {
		if _, err := database.Exec(`INSERT INTO public_listeners (name, protocol, bind_address, port, enabled) VALUES (?, ?, '', ?, 1)`, listener.name, listener.protocol, listener.port); err != nil {
			t.Fatal(err)
		}
	}
	var safe int
	if err := database.QueryRow(`SELECT COUNT(*) FROM public_listeners WHERE bind_address = '127.0.0.1' AND enabled = 0`).Scan(&safe); err != nil || safe != 2 {
		t.Fatalf("safe seeded listeners = %d, error = %v", safe, err)
	}
	// Screenshot fixtures can deliberately enable the seed after assigning its
	// loopback address and unprivileged port; the insert guard doesn't undo this.
	if _, err := database.Exec(`UPDATE public_listeners SET port = 19080, enabled = 1 WHERE name = 'public-http'`); err != nil {
		t.Fatal(err)
	}
	var enabled int
	if err := database.QueryRow(`SELECT enabled FROM public_listeners WHERE name = 'public-http'`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("enabled listener = %d, error = %v", enabled, err)
	}
}
