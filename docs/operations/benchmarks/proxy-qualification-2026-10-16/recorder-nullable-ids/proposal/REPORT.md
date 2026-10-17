# Recorder nullable-ID boxing proposal — source-only

One distinct candidate is justified for a bounded offline screen: convert the recorder inserter's eight `sql.NullInt64` fields to their exact driver values before storing them in `[]any`. `candidate.patch` changes only `internal/db/proxy_request_event_inserter.go`, adding one pure helper and using it at eight argument positions. It is a proposal, not an applied implementation or accepted optimization. No compilation, test, benchmark, profile command, server or traffic workload was run; `git apply --check` passes. No tracked files were edited for this proposal.

Runtime pin is `27c8e31f3826ab5326d941b20644c1f56159d99d`; retained profile pin is `248bad099682878fe398c0ebaad235787ef47858`. The inserter, recorder and rollup source files are byte-identical between those pins. manifest.json binds all local source/profile inputs and the proposed destination hash. Current evidence-only HEAD does not change the attribution.

## Concrete discrepancy

`appendProxyRequestEventArgs` currently boxes each of eight nullable-ID structs into an interface. Pinned Go 1.26.6 then converts each through `database/sql.defaultCheckNamedValue` → `driver.DefaultParameterConverter.ConvertValue` → `sql.NullInt64.Value` before SQLite consumes it. `sql.NullInt64.Value` has only two outcomes and always returns a nil error:

```go
func (n NullInt64) Value() (driver.Value, error) {
    if !n.Valid {
        return nil, nil
    }
    return n.Int64, nil
}
```

Exact local citation: `/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/src/database/sql/sql.go:236-241`. The default converter first accepts already-valid driver values at `driver/types.go:241-244`, otherwise calls Valuer at lines247-255. The default checker is `convert.go:98-100`. Pinned SQLite binds nil as SQL NULL and int64 as sqlite3_bind_int64 at `github.com/mattn/go-sqlite3@v1.14.49/sqlite3.go:2199-2206`. Its pinned Go sources contain no CheckNamedValue or ColumnConverter implementation; `internal/db/open.go:29` selects that SQLite driver. Numbered snapshots and input hashes are retained in source-citations.txt and manifest.json.

The proposal supplies exactly the nil/int64 value the standard converter would produce, avoiding the intermediate nullable struct box. Invalid values remain NULL even when their Int64 payload is nonzero. Valid zero remains int64(0), never NULL. Every valid positive/negative/extreme signed value remains the same int64; database foreign-key/error handling is unchanged. PARAMETER-MAP.md and parameter-map.json enumerate all 23 ordinals and all eight fields' nil/zero/nonzero semantics.

## Allocation scale and ceiling

The committed allocation listing directly attributes these flat sampled bytes to the eight nullable-ID boxing lines:

| Field | Flat sampled bytes |
|---|---:|
| ListenerID | 6291552 |
| RouteID | 5242960 |
| RouteTargetID | 7340144 |
| WafRuleID | 5767256 |
| AgentID | 3145776 |
| CacheRuleID | 7340144 |
| RetryRuleID | 3670072 |
| RetryFailedAgentID | 7864440 |
| Total | 46662344 |

Dividing by the profile's 411,173 whole-lifetime normalized completions gives **113.49 B/completion**, about **3.49% of the recorded 3,247.44 B production/native allocation gap**. This is the targeted sampled source cost, not a measured candidate saving. Eight nominal 16-byte nullable-struct boxes on amd64 give a source-level upper target of roughly 128 B and eight boxes per event; successful primitive int64 boxing may remain, and compiler behavior and actual net allocations still need measurement. No allocated-object saving has been measured.

The proposal does not target the much larger 962.22 B/completion NamedValue slice: `database/sql/convert.go:110` still allocates the same 23×16-entry slice for each full statement. Nor does it remove string/time boxing, SQLite parameter binding, sqlite3_step or cgo. The 197.64 B argument-boxing node contains the nullable lines and other fields; it must not be added to them. SQLite 12.79 µs and event-insert 1,201.95 B cumulative profile nodes overlap descendants and cannot be credited as this candidate's gain. No CPU, throughput or parity gain is established or predicted.

## Preserved contract and review boundary

SQL text, all 23 parameter positions, 16-row SQL groups, final remainder processing, 512-event recorder bound, 250 ms schedule and five-second flush deadline remain untouched. The same transaction, statement preparation/execution/close order and cancellation context flow remain in place. There is no change to channel ownership, retries, raw event order/count, rollup inputs, cache touches, schema, policies or persisted value types. The helper has no pointers, shared state, pooling, retention, callbacks or asynchronous work.

Generated queries remain untouched; this only changes the hand-written recorder inserter wrapper. It does not bypass database/sql, reuse its private NamedValue storage, specialize SQL for omitted defaults, or alter the SQLite driver. SCREEN.md proposes one fixed correctness/allocation/timing screen after explicit CPU release and authorization. Until that screen passes, retaining the patch is not recommended. Rejected SQL group-size/hierarchy, host, cache-read-hit and shaper experiments are not reopened.
