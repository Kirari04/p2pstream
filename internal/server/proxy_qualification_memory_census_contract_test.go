//go:build linux

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

func qualificationMemoryCensusTestApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	// Keep the fixture secure independently of the caller's umask, without
	// changing the process-wide umask while other tests may be running.
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-performance.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	app := NewApp(&config.Config{ManagementUIDisabled: true, ConfigDir: dir, PublicCacheDir: filepath.Join(dir, "cache")}, database)
	t.Cleanup(func() {
		app.DirectTransports.closeAll()
		app.AgentTransports.closeAll()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := app.CloseObservabilityRecorder(ctx); err != nil {
			t.Error(err)
		}
	})
	return app, path
}

func qualificationMemoryEventSnapshot(t *testing.T, app *App) ([]string, int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	rows, err := app.DB.QueryContext(ctx, `SELECT id,status_code,error_kind,method,host,path_prefix,request_bytes,response_bytes,retry_count,retry_outcome FROM proxy_request_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	events := []string{}
	for rows.Next() {
		var id, status, requestBytes, responseBytes, retries int64
		var kind, method, host, path, outcome string
		if err := rows.Scan(&id, &status, &kind, &method, &host, &path, &requestBytes, &responseBytes, &retries, &outcome); err != nil {
			t.Fatal(err)
		}
		events = append(events, fmt.Sprintf("%d/%d/%q/%q/%q/%q/%d/%d/%d/%q", id, status, kind, method, host, path, requestBytes, responseBytes, retries, outcome))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var rollup int64
	if err := app.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(requests),0) FROM proxy_request_tuple_rollup_minutes`).Scan(&rollup); err != nil {
		t.Fatal(err)
	}
	return events, rollup
}

func TestQualificationMemoryCensusPreservesEventsAndRollups(t *testing.T) {
	app, path := qualificationMemoryCensusTestApp(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4")
		if r.URL.Path == "/problem" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = w.Write([]byte("body"))
	}))
	t.Cleanup(origin.Close)
	target := directTransportPoolTestTarget(t, 70, origin.URL, time.Second)
	for _, path := range []string{"/ok", "/problem"} {
		writer := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://public.test"+path, nil)
		proxyDirectTargetForTest(app, writer, request, target)
		want := 200
		if path == "/problem" {
			want = 503
		}
		if writer.Code != want || writer.Body.String() != "body" {
			t.Fatalf("status/body=%d/%q", writer.Code, writer.Body.String())
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := app.FlushObservabilityRecorder(ctx); err != nil {
		t.Fatal(err)
	}
	before, rollup := qualificationMemoryEventSnapshot(t, app)
	if len(before) != 2 || rollup != 2 {
		t.Fatalf("baseline events=%v rollup=%d", before, rollup)
	}
	t.Setenv(qualificationMemoryCensusEnv, "1")
	census, err := qualificationMemoryCensusFromEnv(app.DB.DB, path)
	if err != nil {
		t.Fatal(err)
	}
	handler := qualificationRuntimeHandlerWithMemoryCensus(app, census)
	// Default TotalAlloc/event reads do not invoke the optional census.
	ordinary := httptest.NewRecorder()
	handler.ServeHTTP(ordinary, httptest.NewRequest(http.MethodGet, "/?events=1", nil))
	var ordinaryResult qualificationRuntime
	if err := json.Unmarshal(ordinary.Body.Bytes(), &ordinaryResult); err != nil || ordinaryResult.MemoryCensus != nil {
		t.Fatalf("ordinary result census=%v decode=%v", ordinaryResult.MemoryCensus, err)
	}
	for range 2 {
		writer := httptest.NewRecorder()
		handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/?flush=1&events=1&memory_census=1", nil))
		var result qualificationRuntime
		if writer.Code != 200 || json.Unmarshal(writer.Body.Bytes(), &result) != nil || result.MemoryCensus == nil {
			t.Fatalf("census response status=%d body=%s", writer.Code, writer.Body.String())
		}
		c := result.MemoryCensus
		if c.Status != "ok" || c.Error != "" || !result.EventStats || result.PersistedEvents != 2 || result.DroppedEvents != 0 || result.PendingEvents != 0 || result.QueuedEvents != 0 {
			t.Fatalf("census/event terminal=%+v/%+v", c, result)
		}
		if len(c.Files) != 3 || !c.Files[0].Present || c.Files[0].Path != path || c.Files[0].UID != uint32(os.Geteuid()) || c.Files[0].Mode != 0600 || c.Files[0].Links != 1 || c.Files[0].LogicalBytes <= 0 || c.PageCount <= 0 || c.PageSize < 512 || c.JournalMode != "wal" || len(c.Indexes) == 0 || c.ConnectionScope != "one_leased_connection_not_pool_totals" {
			t.Fatalf("census metadata=%+v", c)
		}
		var enabled int
		if err := app.DB.QueryRowContext(ctx, "SELECT sqlite_compileoption_used('ENABLE_DBSTAT_VTAB')").Scan(&enabled); err != nil {
			t.Fatal(err)
		}
		want := "unsupported"
		if enabled == 1 {
			want = "available_not_collected"
		}
		if c.DBStatState != want || c.DBStatReason == "" {
			t.Fatalf("dbstat state=%q want%q reason=%q", c.DBStatState, want, c.DBStatReason)
		}
		encoded, err := json.Marshal(c)
		if err != nil || len(encoded) > qualificationMemoryCensusMaxBytes {
			t.Fatalf("census encoding bytes=%d error=%v", len(encoded), err)
		}
		after, afterRollup := qualificationMemoryEventSnapshot(t, app)
		if !reflect.DeepEqual(before, after) || afterRollup != rollup {
			t.Fatalf("census changed recorded evidence: %v/%d -> %v/%d", before, rollup, after, afterRollup)
		}
		if stats := app.DB.Stats(); stats.InUse != 0 {
			t.Fatalf("census retained connection: %+v", stats)
		}
	}
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/?memory_census=1&close_idle_agent_transports=1", nil))
	if invalid.Code != 400 {
		t.Fatalf("mixed control status=%d", invalid.Code)
	}
}

func TestQualificationMemoryCensusFileMetadata(t *testing.T) {
	for _, name := range []string{"missing_companions", "symlink_wal", "directory_shm", "hardlink_wal", "permissive_main", "missing_main"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent-performance.db")
			if err := os.WriteFile(path, []byte("metadata-only fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "symlink_wal":
				if err := os.Symlink(path, path+"-wal"); err != nil {
					t.Fatal(err)
				}
			case "directory_shm":
				if err := os.Mkdir(path+"-shm", 0700); err != nil {
					t.Fatal(err)
				}
			case "hardlink_wal":
				if err := os.Link(path, path+"-wal"); err != nil {
					t.Fatal(err)
				}
			case "permissive_main":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "missing_main":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			files, err := qualificationMemoryFiles(path)
			if name != "missing_companions" {
				if err == nil {
					t.Fatalf("unsafe %s accepted: %+v", name, files)
				}
				return
			}
			if err != nil || len(files) != 3 || !files[0].Present || files[1].Present || files[2].Present || files[1].Name != "wal" || files[2].Name != "shm" {
				t.Fatalf("normal absent companions: %+v error=%v", files, err)
			}
		})
	}
}

func TestQualificationMemoryCensusOptInAndBounds(t *testing.T) {
	for _, name := range []string{"disabled", "invalid_mode", "invalid_path", "directory_permissions", "canceled", "database_identity", "index_rows", "index_name", "error_bounds", "busy_owner", "expected_main_identity"} {
		t.Run(name, func(t *testing.T) {
			if name == "disabled" {
				t.Setenv(qualificationMemoryCensusEnv, "")
				census, err := qualificationMemoryCensusFromEnv(nil, "not a path")
				if census != nil || err != nil {
					t.Fatalf("disabled census=%v error=%v", census, err)
				}
				return
			}
			if name == "invalid_mode" {
				t.Setenv(qualificationMemoryCensusEnv, "0")
				if _, err := qualificationMemoryCensusFromEnv(nil, ""); err == nil {
					t.Fatal("invalid explicit mode accepted")
				}
				return
			}
			if name == "error_bounds" {
				for _, test := range []struct{ name, stage, message, want string }{
					{"short_unicode", "files", "é", "files: é"},
					{"exact_256", "s", strings.Repeat("x", 253), "s: " + strings.Repeat("x", 253)},
					{"prefix_counts_toward_256", "scalar", strings.Repeat("x", 300), "scalar: " + strings.Repeat("x", 248)},
					{"cut_before_multibyte_rune", "files", strings.Repeat("x", 248) + "界", "files: " + strings.Repeat("x", 248)},
					{"invalid_prefix_and_message", "b\xff", "m\xfe\xffz", "b\uFFFD: m\uFFFDz"},
					{"invalid_rune_at_cut", "files", strings.Repeat("x", 248) + "\xffrest", "files: " + strings.Repeat("x", 248)},
					{"long_stage_is_bounded", strings.Repeat("é", 200), "message", strings.Repeat("é", 128)},
				} {
					got := qualificationMemoryCensusFailure(test.stage, test.message)
					if got != test.want || len(got) > 256 || !utf8.ValidString(got) {
						t.Fatalf("%s: got%q (%dbytes valid%v) want%q", test.name, got, len(got), utf8.ValidString(got), test.want)
					}
				}
				return
			}
			app, path := qualificationMemoryCensusTestApp(t)
			if name == "directory_permissions" {
				if err := os.Chmod(filepath.Dir(path), 0770); err != nil {
					t.Fatal(err)
				}
				if _, err := newQualificationMemoryCensus(app.DB.DB, path); err == nil {
					t.Fatal("group-writable database directory accepted")
				}
				return
			}
			if name == "invalid_path" {
				if _, err := newQualificationMemoryCensus(app.DB.DB, filepath.Join(filepath.Dir(path), "other.db")); err == nil {
					t.Fatal("non-fixture path accepted")
				}
				return
			}
			if name == "database_identity" {
				dir := t.TempDir()
				if err := os.Chmod(dir, 0700); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(dir, "agent-performance.db")
				if err := os.WriteFile(path, []byte("must not be opened or read"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			census, err := newQualificationMemoryCensus(app.DB.DB, path)
			if err != nil {
				t.Fatal(err)
			}
			if name == "busy_owner" {
				// This test owns the pre-held gate. The rejected capture must not
				// clear it or lease a DB connection on behalf of another owner.
				census.busy.Store(true)
				defer census.busy.Store(false)
			}
			if name == "expected_main_identity" {
				// Exercise the recorded-inode comparison without renaming or
				// replacing a live SQLite file; this is not a filesystem-race proof.
				census.inode ^= 1
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if name == "index_rows" {
				for i := range qualificationMemoryCensusMaxIndexes + 1 {
					if _, err := app.DB.ExecContext(ctx, fmt.Sprintf("CREATE INDEX census_%02d ON proxy_request_events(status_code)", i)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if name == "index_name" {
				if _, err := app.DB.ExecContext(ctx, "CREATE INDEX "+strings.Repeat("c", 129)+" ON proxy_request_events(status_code)"); err != nil {
					t.Fatal(err)
				}
			}
			if name == "canceled" {
				cancel()
			}
			result := census.capture(ctx)
			if result.Status != "error" || result.Error == "" {
				t.Fatalf("unsupported %s returned %+v", name, result)
			}
			if name == "index_rows" && !strings.Contains(result.Error, "row bound exceeded") {
				t.Fatalf("row-limit result=%+v", result)
			}
			if name == "index_name" && !strings.Contains(result.Error, "unsupported index metadata") {
				t.Fatalf("name-limit result=%+v", result)
			}
			if name == "database_identity" && !strings.Contains(result.Error, "database_identity") {
				t.Fatalf("identity result=%+v", result)
			}
			if name == "busy_owner" && (!census.busy.Load() || !strings.HasPrefix(result.Error, "busy:")) {
				t.Fatalf("busy capture cleared another owner or failed elsewhere: %+v busy=%v", result, census.busy.Load())
			}
			if name == "expected_main_identity" && !strings.Contains(result.Error, "main DB identity changed") {
				t.Fatalf("main identity mismatch was not detected: %+v", result)
			}
			if len(result.Error) > 256 || !utf8.ValidString(result.Error) {
				t.Fatalf("invalid complete census error: %q", result.Error)
			}
			if stats := app.DB.Stats(); stats.InUse != 0 {
				t.Fatalf("failed census retained connection: %+v", stats)
			}
		})
	}
}
