# Bounded wiring and build result

The approved wiring window and compile-only window completed once. H1 continuation passed all 20 cases; the existing arm-outcome producer/mapping test passed seven outcome cases and four revisit cases, emitting exactly 106 hash keys. Shell and Python syntax checks passed. Every invocation exited naturally with status zero; none was forced and no owned process-group member remains.

Pinned Go 1.26.6 compiled the agent and server test endpoints using the declared CPUs8–11/GOMAXPROCS2 and normalized environment. Preflight and both binary metadata reads also returned natural zero. No endpoint binary was executed. No WAN traffic ran. The startup UNBUILT guard remains pending separate root review/authorization.

The build receipt SHA256 is ad0dbfb3adafab686a4fc84f75effaee57819f2694ae644ada901a192604ad62. Actual agent binary SHA256 is 8d38b8c8e6f18250d96ad07fa8db4ce11ce65f1a8734c04cf447b9909131f1d7 (21,978,002 bytes); server is f74384c319674212123832401069417a3cae31c35997545a061788e576b47c6e (60,251,872 bytes). This is a diagnostic build pinned to production 27c8e31 plus the reviewed observer; it does not incorporate the unrelated recorder change.

POST-AUDIT.json records 513 unchanged source/checkpoint hashes, no remaining owned groups, and 92,108,071 task bytes before this short summary, below the 100 MiB bound. Original 459 tested files remain unchanged; the compile inventory adds only the audited stats file. The earlier 170/170 focused agent normal and race tests retain their original scope. Successful compilation is not a server runtime test or performance qualification.

Original raw verdicts/counters, historical rejects and sustained performance gates remain unchanged. The receipt verifier source has been reviewed and syntax-checked but was not invoked in this compile-only window. All runtime ownership has been released.
