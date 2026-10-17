# Nonprinting exact-value scan

The selection scan recovered exact private-key bytes and authentication-like values from excluded, closed-run files. It compared those values against every selected candidate without printing values or matched content.

The frozen report records zero selected hit artifacts and `accepted:true`. Any genuine hit would have removed the whole artifact; no raw artifact would have been rewritten or sanitized. Public route identifiers and public test certificates are not treated as authentication secrets.

The process-local trace bearer was unavailable because it was never persisted. Exact comparison does not cover transformed encodings or unknown secret forms. `SECRET-SCAN.json` is the machine-readable result.
