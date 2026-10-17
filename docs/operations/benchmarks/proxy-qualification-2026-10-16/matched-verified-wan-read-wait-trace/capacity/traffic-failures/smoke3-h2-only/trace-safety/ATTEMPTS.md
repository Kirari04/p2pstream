# Audit attempts

Both content-scan invocations returned status `0`. The first report is retained
as `export-safety-report.attempt-1.json`. Its safety result was valid, but its
copied H1 packaging prose described all reductions as rejected and quoted the
old parsed-size total. The final source records the actual H2 original analyzer
statuses (sequence 9/11 status 1, sequence 13 status 0), removes the stale size,
and preserves the separately corrected-offline status as a distinct fact.
No scan gate or secret-handling control changed.

