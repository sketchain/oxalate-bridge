package cpaquota

import _ "embed"

// SchemaSQL is the reference copy of cpa-quota's tables (schema.sql). The
// bridge never migrates the real cpa-quota schema; this is used by tests and
// by `bridge seed-demo` to create a demonstration schema when none exists.
//
//go:embed schema.sql
var SchemaSQL string
