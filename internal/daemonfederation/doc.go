// Package daemonfederation aggregates jobs from self-contained Kitsoki daemon
// workers for the runstatus server. It sits between webconfig, which validates
// operator-owned worker and SSH settings, and runstatus, which exposes one
// namespaced jobs and worker-health view.
//
// # Polling and failure semantics
//
// Pool polls workers concurrently and caches a snapshot briefly so one UI
// refresh does not issue duplicate worker calls. A worker that has never
// answered is offline. A previously online worker that misses a poll is
// degraded and keeps its last known jobs, making partial failure visible
// without hiding durable operator context.
//
// # Worked example
//
// A worker named build-vm exposes its loopback-only daemon on port 7777. Its
// SSH tunnel forwards local port 17777 to that daemon, so a returned run URL of
// /s/job-1 is projected as http://127.0.0.1:17777/s/job-1 and tagged with the
// build-vm worker identity.
//
// # Lifecycle
//
// TunnelManager starts one supervised SSH process per tunneled worker and
// retries dropped processes until Close is called or the parent context ends.
// Pool is safe for concurrent callers; Config should be validated once before
// constructing either component.
//
// # Non-goals
//
//   - No job scheduler or placement policy. This package observes workers;
//     dispatch remains an explicit higher-level decision.
//   - No SSH key generation, host-key enrollment, or secret distribution.
//     Trust material remains operator-owned to avoid silent trust changes.
//   - No VM provisioning. Ephemeral capacity is a later provisioner boundary,
//     while each provisioned machine still presents the same daemon contract.
//
// # Reference
//
// See docs/guide/development/daemon.md for configuration, security, deployment,
// and placement guidance.
package daemonfederation
