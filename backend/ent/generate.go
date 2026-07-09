package ent

// Run `go generate ./ent` (or `make generate`) to regenerate the typed client
// from the schemas in ./schema. The CLI is pinned to a version so its own
// (internally consistent) dependency graph is used, instead of letting the main
// module resolve a possibly-broken "latest" transitive dep.
//go:generate go run entgo.io/ent/cmd/ent@v0.14.6 generate --feature sql/upsert ./schema
