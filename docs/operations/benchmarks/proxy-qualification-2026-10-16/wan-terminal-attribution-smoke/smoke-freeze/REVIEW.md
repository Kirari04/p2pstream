# Final smoke source freeze

Root reports the actual receipt verifier returned natural zero for the approved receipt and actual compiled binaries/source references. The receipt hash is ad0dbfb3adafab686a4fc84f75effaee57819f2694ae644ada901a192604ad62.

The only runner edit replaces UNBUILT with that literal hash. blocked-runner.sh preserves the prior bytes and UNBLOCK.diff shows the one-line change. All previous source checkpoints and build/test receipts remain untouched. COMMANDS.json freezes the actual runner, source reference, binary paths, certificate identity, one H1 command, conditional H2 command and H1 continuation command. Each arm has a 900-second outer timeout and 10-second TERM grace; continuation is bounded to 20 seconds. Every existing inner lifecycle, endpoint, payload and resource gate remains in place. A forced or failed command rejects; no retry is allowed.

H2 remains withheld unless H1 returns naturally zero, independent arm validation passes, and the existing adapted H1 continuation validator returns naturally zero. Both arms retain 4 MiB payloads, 16 transfers each, 80 ms RTT and all predeclared small-diagnostic settings. This cannot clear the original sustained performance gate. The certificate must still pass the real-clock preflight at execution time. Preserve all results and stop on the first failure, missing evidence, source mismatch or artifact-budget breach.

No compilation, test, endpoint, or WAN command was run while making this source-only freeze. Traffic remains held until root completes its docs-build window and explicitly transfers runtime.
