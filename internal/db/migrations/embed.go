package migrations

import "embed"

// FS contains the SQL migrations embedded into the p2pstream binary so runtime
// database upgrades do not depend on external migration files being present.
//
//go:embed *.sql
var FS embed.FS
