# Benchmark evidence

These directories preserve historical benchmark inputs, source excerpts, and results. Archived Go files may be sparse overlays rather than standalone packages; use each packet's recorded source inventory and reproduction instructions.

The local `go.mod` is a documentary module boundary. It excludes these archives from the production root module's recursive Go build, test, vet, and CodeQL package discovery without changing any packet's source bytes or checksums. It does not declare the archived experiments to be independently buildable modules.
