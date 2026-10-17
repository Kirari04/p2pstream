# All 23 parameter positions

SQL text and parameter count/order remain unchanged. Only the eight sql.NullInt64 inputs change Go representation before database/sql conversion.

| Position | Field | Original Go type | Candidate argument |
|---:|---|---|---|
| 1 | OccurredAt | `time.Time` | `arg.OccurredAt` |
| 2 | StatusCode | `int64` | `arg.StatusCode` |
| 3 | DurationMs | `int64` | `arg.DurationMs` |
| 4 | ErrorKind | `string` | `arg.ErrorKind` |
| 5 | Method | `string` | `arg.Method` |
| 6 | Host | `string` | `arg.Host` |
| 7 | PathPrefix | `string` | `arg.PathPrefix` |
| 8 | ListenerID | `sql.NullInt64` | `proxyRequestEventIDValue(arg.ListenerID)` |
| 9 | RouteID | `sql.NullInt64` | `proxyRequestEventIDValue(arg.RouteID)` |
| 10 | RouteTargetID | `sql.NullInt64` | `proxyRequestEventIDValue(arg.RouteTargetID)` |
| 11 | WafRuleID | `sql.NullInt64` | `proxyRequestEventIDValue(arg.WafRuleID)` |
| 12 | WafAction | `string` | `arg.WafAction` |
| 13 | AgentID | `sql.NullInt64` | `proxyRequestEventIDValue(arg.AgentID)` |
| 14 | RequestBytes | `int64` | `arg.RequestBytes` |
| 15 | ResponseBytes | `int64` | `arg.ResponseBytes` |
| 16 | CacheRuleID | `sql.NullInt64` | `proxyRequestEventIDValue(arg.CacheRuleID)` |
| 17 | CacheStatus | `string` | `arg.CacheStatus` |
| 18 | CacheBytes | `int64` | `arg.CacheBytes` |
| 19 | RetryRuleID | `sql.NullInt64` | `proxyRequestEventIDValue(arg.RetryRuleID)` |
| 20 | RetryCount | `int64` | `arg.RetryCount` |
| 21 | RetryOutcome | `string` | `arg.RetryOutcome` |
| 22 | RetryErrorKind | `string` | `arg.RetryErrorKind` |
| 23 | RetryFailedAgentID | `sql.NullInt64` | `proxyRequestEventIDValue(arg.RetryFailedAgentID)` |

For **each** changed field (ListenerID, RouteID, RouteTargetID, WafRuleID, AgentID, CacheRuleID, RetryRuleID, RetryFailedAgentID), pinned sql.NullInt64.Value at sql.go:236 has these exact semantics:

| Valid | Int64 payload | Original converted SQL driver value | Candidate value |
|---|---|---|---|
| false | 0 | nil → SQL NULL | nil → SQL NULL |
| false | any nonzero signed value | nil → SQL NULL; payload ignored | nil → SQL NULL; payload ignored |
| true | 0 | int64(0), not NULL | int64(0), not NULL |
| true | any nonzero signed value, including negative and int64 extrema | same int64 | same int64 |

The original Value method always returns a nil error. Both candidate results are already legal driver.Value inputs, so the default parameter converter returns them directly; SQLite binds the same NULL/int64 at the same ordinal. Zero IDs are not silently changed to NULL, and invalid nonzero payloads are not exposed. Valid foreign-key violations retain their database errors. All other parameter values, including time, strings, sizes and counters, are untouched.
