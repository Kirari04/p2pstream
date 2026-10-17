# Reviewed-input pressure launch plan — executed once

Candidate: `27c8e31f3826ab5326d941b20644c1f56159d99d`.

The exact command was executed once after the coordinator granted the full build/cohort window:

```sh
bash tmp/pressure-final-readiness-20261017/LAUNCH-27c8e31.sh
```

The launch script verifies all frozen v3 sources, pins the attempt to CPUs 0–15, and invokes the pressure-only checkpoint with the explicit pinned Go 1.26.6 executable. It allocates the fresh output `tmp/final-qualification/27c8e31f3826ab5326d941b20644c1f56159d99d/pressure-attempt-1`; outer stdout/stderr/status remain in sibling files. Existing output is rejected. No automatic retry is included.

V3 changes only the unexecuted checkpoint: each of three clean-archive `go test -c` commands has a 600-second deadline and ten-second kill grace; its manifest records those commands. Aggregate compile allowance is at most 1,830 seconds including kill grace. The inner full pressure attempt retains the 4,800-second cap and twenty-second cleanup grace. The outer whole build/cohort cap is 7,200 seconds with thirty-five-second kill grace, leaving room for all build/runtime bounds and normal archive/validation overhead. The pressure runner and actual scope contract are unchanged from the successful v2 scope validation.

`v3-per-build-timeouts.patch` is the exact v2→v3 diff. `OFFLINE-FROZEN-SOURCE-v3.sha256` binds final source; the v3 checkpoint passes `bash -n`. All v2 offline schema/lifecycle checks and actual scope receipts remain separately recorded. The clean archive build and original pressure cohort subsequently passed; see `REPORT.md` and `independent-result.json`.

Read-only Git-tree metadata estimates current archive/extracted storage conservatively at about 500.5 MiB, and legacy archive/extracted storage at about 33.5 MiB. Reserving 192 MiB for binaries and 256 MiB for raw/log evidence yields about **982 MiB** total, plus incremental growth in the ordinary shared Go content-addressed cache. There is no duplicate private compiler cache. At estimation time the filesystem had about **12.8 GiB free**. These are planning estimates rather than a filesystem quota; raw logs and incremental shared cache depend on execution. A 2 GiB free-space planning reserve comfortably exceeds the approximately 1 GiB archive/source/binary/raw estimate. Full calculations and historical binary sizes: `storage-estimate.json`.

The actual scope contract establishes original-topology scope creation, cgroup envelopes and natural joins for finite substituted payloads. It does not establish Go application shutdown or the full workload. Full pressure success still requires the fresh clean builds, unchanged manifest-bound actual inputs, the original thirteen literal-true pressure gates, strict telemetry, all six natural endpoint/load/namespace owner exits, zero outer/inner statuses, complete raw/provenance evidence and final attestation.
