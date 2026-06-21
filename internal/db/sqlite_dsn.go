package db

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

func normalizeSQLiteDSN(databaseURL string) (string, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		databaseURL = "file:p2pstream.db?mode=rwc"
	}

	if strings.HasPrefix(databaseURL, "file:") {
		prefix, rawQuery, _ := strings.Cut(databaseURL, "?")
		values, err := url.ParseQuery(rawQuery)
		if err != nil {
			return "", fmt.Errorf("invalid sqlite database URL %q: %w", databaseURL, err)
		}
		if values.Get("mode") == "" && prefix != "file::memory:" {
			values.Set("mode", "rwc")
		}
		applySQLitePragmas(values)
		return prefix + "?" + values.Encode(), nil
	}

	values := url.Values{}
	values.Set("mode", "rwc")
	applySQLitePragmas(values)
	return "file:" + databaseURL + "?" + values.Encode(), nil
}

func applySQLitePragmas(values url.Values) {
	values.Set("_journal_mode", "WAL")
	values.Set("_synchronous", "NORMAL")
	values.Set("_busy_timeout", "10000")
	values.Set("_fk", "1")
	values.Set("cache", "private")
}

func sqliteFilePathFromDSN(dsn string) (string, bool) {
	if !strings.HasPrefix(dsn, "file:") {
		return "", false
	}
	prefix, rawQuery, _ := strings.Cut(dsn, "?")
	values, err := url.ParseQuery(rawQuery)
	if err == nil && strings.EqualFold(values.Get("mode"), "memory") {
		return "", false
	}
	path := strings.TrimPrefix(prefix, "file:")
	if path == "" || path == ":memory:" || strings.HasPrefix(path, ":memory:") {
		return "", false
	}
	if unescaped, err := url.PathUnescape(path); err == nil {
		path = unescaped
	}
	return filepath.Clean(path), true
}
