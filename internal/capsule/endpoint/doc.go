// Package endpoint owns machine-wide leases for runtime listener endpoints.
//
// A lease binds a concrete protocol/address/port to one runtime generation,
// but callers identify their endpoint by service and logical role. Runtime
// providers receive the assigned endpoint and must attest the listener before
// readiness can succeed; they must not probe or select fallback ports.
package endpoint
