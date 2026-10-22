// Package engineapi holds the wire types of the engine's /v1 HTTP API.
package engineapi

// APIVersion must match engine/src/forja_engine/api/version.py.
// The core refuses an engine whose major version differs.
const APIVersion = "1.1.0"
