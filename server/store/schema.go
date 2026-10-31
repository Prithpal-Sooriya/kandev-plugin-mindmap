// Package store will hold the SQLite store (Phase 1a). For now it only embeds
// the v1 schema so the backend and tests share one copy.
package store

import _ "embed"

// SchemaV1 is migration v1. Apply it in one transaction when
// PRAGMA user_version = 0, then set user_version = 1.
//
//go:embed schema.sql
var SchemaV1 string

// SchemaVersion is the user_version after all migrations in this package.
const SchemaVersion = 1
