//go:build linux

package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	qualificationMemoryCensusEnv           = "P2PSTREAM_QUALIFICATION_MEMORY_CENSUS"
	qualificationMemoryCensusTimeout       = 2 * time.Second
	qualificationMemoryCensusMaxIndexes    = 64
	qualificationMemoryCensusMaxBytes      = 32 << 10
	qualificationMemoryCensusMaxErrorBytes = 256
)

type qualificationMemoryFile struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Present        bool   `json:"present"`
	LogicalBytes   int64  `json:"logical_bytes"`
	AllocatedBytes int64  `json:"allocated_bytes"`
	Device         uint64 `json:"device"`
	Inode          uint64 `json:"inode"`
	UID            uint32 `json:"uid"`
	Mode           uint32 `json:"mode"`
	Links          uint64 `json:"links"`
}

type qualificationMemoryIndex struct {
	Name    string `json:"name"`
	Unique  bool   `json:"unique"`
	Origin  string `json:"origin"`
	Partial bool   `json:"partial"`
}

type qualificationMemoryHeap struct {
	TotalAlloc   uint64 `json:"total_alloc_bytes"`
	HeapAlloc    uint64 `json:"heap_alloc_bytes"`
	HeapInuse    uint64 `json:"heap_inuse_bytes"`
	HeapIdle     uint64 `json:"heap_idle_bytes"`
	HeapReleased uint64 `json:"heap_released_bytes"`
	HeapSys      uint64 `json:"heap_sys_bytes"`
	NumGC        uint32 `json:"num_gc"`
	PauseNS      uint64 `json:"pause_total_ns"`
}

type qualificationMemoryCensusReceipt struct {
	Status            string                     `json:"status"`
	Error             string                     `json:"error"`
	StartedUTC        time.Time                  `json:"started_utc"`
	DurationNS        int64                      `json:"duration_ns"`
	ConnectionScope   string                     `json:"connection_scope"`
	ObservationScope  string                     `json:"observation_scope"`
	Files             []qualificationMemoryFile  `json:"files"`
	PageSize          int64                      `json:"page_size"`
	PageCount         int64                      `json:"page_count"`
	FreelistCount     int64                      `json:"freelist_count"`
	CacheSize         int64                      `json:"cache_size"`
	MmapSize          int64                      `json:"mmap_size"`
	JournalMode       string                     `json:"journal_mode"`
	WALAutocheckpoint int64                      `json:"wal_autocheckpoint"`
	Indexes           []qualificationMemoryIndex `json:"indexes"`
	DBStatState       string                     `json:"dbstat_state"`
	DBStatReason      string                     `json:"dbstat_reason"`
	Heap              qualificationMemoryHeap    `json:"heap"`
}

// This holds only a fixture-owned path and already-open pool. It owns no
// separate database/pool, long-lived file handles or goroutines, or production API.
// Leasing may use the already-open pool's normal connection creation policy.
type qualificationMemoryCensus struct {
	database *sql.DB
	path     string
	device   uint64
	inode    uint64
	busy     atomic.Bool
}

func qualificationMemoryCensusFromEnv(database *sql.DB, path string) (*qualificationMemoryCensus, error) {
	value := os.Getenv(qualificationMemoryCensusEnv)
	if value == "" {
		return nil, nil
	}
	if value != "1" {
		return nil, fmt.Errorf("%s must be unset/empty or exactly 1", qualificationMemoryCensusEnv)
	}
	return newQualificationMemoryCensus(database, path)
}

func newQualificationMemoryCensus(database *sql.DB, path string) (*qualificationMemoryCensus, error) {
	if database == nil {
		return nil, errors.New("memory census requires the already-open fixture DB")
	}
	if len(path) > 1024 || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != "agent-performance.db" {
		return nil, errors.New("memory census requires the exact absolute fixture DB path")
	}
	directory := filepath.Dir(path)
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil || canonical != directory {
		return nil, errors.New("memory census directory must be canonical without symlinks")
	}
	meta, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	stat, ok := meta.Sys().(*syscall.Stat_t)
	if !ok || !meta.IsDir() || meta.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Geteuid()) || meta.Mode().Perm()&0022 != 0 {
		return nil, errors.New("memory census directory has unexpected ownership/type/permissions")
	}
	files, err := qualificationMemoryFiles(path)
	if err != nil {
		return nil, err
	}
	return &qualificationMemoryCensus{database: database, path: path, device: files[0].Device, inode: files[0].Inode}, nil
}

// Exactly three Lstat operations; no file contents or caller-supplied paths are
// read. WAL/SHM absence is normal. The main file identity is checked on capture.
func qualificationMemoryFiles(path string) ([]qualificationMemoryFile, error) {
	files := make([]qualificationMemoryFile, 0, 3)
	for _, spec := range []struct{ name, suffix string }{{"main", ""}, {"wal", "-wal"}, {"shm", "-shm"}} {
		file := qualificationMemoryFile{Name: spec.name, Path: path + spec.suffix}
		info, err := os.Lstat(file.Path)
		if errors.Is(err, os.ErrNotExist) && spec.name != "main" {
			files = append(files, file)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s metadata: %w", spec.name, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Mode().Perm() != 0600 || stat.Blocks < 0 || stat.Blocks > math.MaxInt64/512 {
			return nil, fmt.Errorf("%s metadata has unexpected type/owner/links/mode/blocks", spec.name)
		}
		file.Present = true
		file.LogicalBytes = info.Size()
		file.AllocatedBytes = stat.Blocks * 512
		file.Device = uint64(stat.Dev)
		file.Inode = stat.Ino
		file.UID = stat.Uid
		file.Mode = uint32(info.Mode().Perm())
		file.Links = uint64(stat.Nlink)
		files = append(files, file)
	}
	return files, nil
}

// Bound the complete prefixed message, including malformed UTF-8 from either
// source. Truncation removes an incomplete terminal rune, never an interior byte.
func qualificationMemoryCensusFailure(stage, message string) string {
	value := strings.ToValidUTF8(stage+": "+message, "\uFFFD")
	if len(value) > qualificationMemoryCensusMaxErrorBytes {
		value = value[:qualificationMemoryCensusMaxErrorBytes]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func (c *qualificationMemoryCensus) capture(parent context.Context) *qualificationMemoryCensusReceipt {
	started := time.Now()
	result := &qualificationMemoryCensusReceipt{Status: "error", StartedUTC: started.UTC(), ConnectionScope: "one_leased_connection_not_pool_totals", ObservationScope: "sequential_metadata_after_admin_flush_not_atomic_or_residency_proof", Files: []qualificationMemoryFile{}, Indexes: []qualificationMemoryIndex{}, DBStatState: "not_observed"}
	fail := func(stage string, err error) *qualificationMemoryCensusReceipt {
		result.Status = "error"
		result.Error = qualificationMemoryCensusFailure(stage, err.Error())
		return result
	}
	owned := false
	defer func() {
		if owned {
			defer c.busy.Store(false)
		}
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		result.Heap = qualificationMemoryHeap{TotalAlloc: stats.TotalAlloc, HeapAlloc: stats.HeapAlloc, HeapInuse: stats.HeapInuse, HeapIdle: stats.HeapIdle, HeapReleased: stats.HeapReleased, HeapSys: stats.HeapSys, NumGC: stats.NumGC, PauseNS: stats.PauseTotalNs}
		result.DurationNS = time.Since(started).Nanoseconds()
		encoded, err := json.Marshal(result)
		if err != nil || len(encoded) > qualificationMemoryCensusMaxBytes {
			result.Status = "error"
			result.Error = "receipt exceeds finite encoding bound"
			result.Files = nil
			result.Indexes = nil
		}
	}()
	if !c.busy.CompareAndSwap(false, true) {
		return fail("busy", errors.New("another census is active"))
	}
	owned = true
	ctx, cancel := context.WithTimeout(parent, qualificationMemoryCensusTimeout)
	defer cancel()
	connection, err := c.database.Conn(ctx)
	if err != nil {
		return fail("lease", err)
	}
	defer func() {
		if err := connection.Close(); err != nil {
			fail("return_connection", err)
		}
	}()
	if err := qualificationMemoryDatabaseIdentity(ctx, connection, c.path); err != nil {
		return fail("database_identity", err)
	}
	for _, item := range []struct {
		query string
		value *int64
	}{
		{"PRAGMA main.page_size", &result.PageSize}, {"PRAGMA main.page_count", &result.PageCount}, {"PRAGMA main.freelist_count", &result.FreelistCount},
		{"PRAGMA main.cache_size", &result.CacheSize}, {"PRAGMA main.mmap_size", &result.MmapSize}, {"PRAGMA wal_autocheckpoint", &result.WALAutocheckpoint},
	} {
		if err := connection.QueryRowContext(ctx, item.query).Scan(item.value); err != nil {
			return fail(item.query, err)
		}
	}
	if err := connection.QueryRowContext(ctx, "PRAGMA main.journal_mode").Scan(&result.JournalMode); err != nil {
		return fail("journal_mode", err)
	}
	if result.PageSize < 512 || result.PageSize > 65536 || result.PageCount < 0 || result.FreelistCount < 0 || result.FreelistCount > result.PageCount || result.MmapSize < 0 || result.WALAutocheckpoint < 0 || len(result.JournalMode) > 16 {
		return fail("scalar_bounds", errors.New("unsupported SQLite scalar metadata"))
	}
	indexes, err := qualificationMemoryIndexes(ctx, connection)
	if err != nil {
		return fail("indexes", err)
	}
	result.Indexes = indexes
	var dbstat int
	if err := connection.QueryRowContext(ctx, "SELECT sqlite_compileoption_used('ENABLE_DBSTAT_VTAB')").Scan(&dbstat); err != nil {
		return fail("dbstat_availability", err)
	}
	switch dbstat {
	case 0:
		result.DBStatState = "unsupported"
		result.DBStatReason = "SQLite build lacks ENABLE_DBSTAT_VTAB; no build tag or scan added"
	case 1:
		result.DBStatState = "available_not_collected"
		result.DBStatReason = "B-tree/per-page scan deliberately outside this finite metadata census"
	default:
		return fail("dbstat_availability", errors.New("unsupported compile-option result"))
	}
	files, err := qualificationMemoryFiles(c.path)
	if err != nil {
		return fail("files", err)
	}
	if files[0].Device != c.device || files[0].Inode != c.inode {
		return fail("files", errors.New("main DB identity changed since fixture setup"))
	}
	result.Files = files
	if err := ctx.Err(); err != nil {
		return fail("deadline", err)
	}
	result.Status = "ok"
	return result
}

func qualificationMemoryDatabaseIdentity(ctx context.Context, connection *sql.Conn, path string) error {
	rows, err := connection.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	count := 0
	for rows.Next() {
		count++
		if count > 4 {
			return errors.New("database_list row bound exceeded")
		}
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return err
		}
		if name == "temp" && file == "" {
			continue
		}
		if name != "main" || found || file != path {
			return errors.New("unexpected attached database or main DB path")
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !found {
		return errors.New("main DB identity missing")
	}
	return nil
}

func qualificationMemoryIndexes(ctx context.Context, connection *sql.Conn) ([]qualificationMemoryIndex, error) {
	rows, err := connection.QueryContext(ctx, "PRAGMA main.index_list('proxy_request_events')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	indexes := make([]qualificationMemoryIndex, 0, 8)
	for rows.Next() {
		if len(indexes) == qualificationMemoryCensusMaxIndexes {
			return nil, errors.New("index_list row bound exceeded")
		}
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return nil, err
		}
		if len(name) == 0 || len(name) > 128 || strings.ContainsAny(name, "\x00\r\n") || (unique != 0 && unique != 1) || (partial != 0 && partial != 1) || (origin != "c" && origin != "u" && origin != "pk") {
			return nil, errors.New("unsupported index metadata")
		}
		indexes = append(indexes, qualificationMemoryIndex{Name: name, Unique: unique == 1, Origin: origin, Partial: partial == 1})
	}
	return indexes, rows.Err()
}
