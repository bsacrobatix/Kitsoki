// Package corpusproof independently arms corpus-case.v1 candidates with
// baseline-RED and fix-GREEN evidence. It sits after source normalization and
// before corpus admission: adapters describe a candidate, while this package
// decides whether its explicit oracle is reproducible in isolated fixtures.
//
// # Algorithm
//
// Executor validates the wire-compatible ProofInput, canonicalizes and hashes
// its oracle, then asks its FixtureOpener for separate baseline and fix
// workspaces. Its OracleRunner runs the same oracle in each workspace. A proof
// is admitted only when the baseline fails and the fix succeeds under the same
// non-empty environment fingerprint.
//
// # Worked example
//
// A normalized candidate has baseline_ref "abc", fix_ref "def", and an oracle
// whose canonical JSON says how to run one regression test. The opener creates
// one capsule (or repo-history fixture) for abc and another for def. If the
// runner reports exit 1 for abc and exit 0 for def, both with fingerprint
// "go1.24-linux", Prove returns a Proof containing the command, output, hash,
// and fingerprint. If abc exits 0, Prove returns a Rejection with
// KindAlreadyGreen instead.
//
// # Contracts
//
// The zero value of Executor is not useful: callers must inject both
// FixtureOpener and OracleRunner. The package never shells out, fetches a
// repository, or calls a model itself. Implementations own those effects and
// must provide isolated workspaces. Executor is safe for concurrent calls when
// its dependencies are safe for concurrent calls.
//
// # Non-goals
//
//   - Package corpusproof does not discover candidates or interpret a
//     story-specific oracle; that would let an admission path certify itself.
//   - Package corpusproof does not materialize remote history directly; capsule
//     and repo-history executors own source-specific isolation.
//   - Package corpusproof does not retry a green baseline into a red result;
//     such a case is an explicit rejection, not training evidence.
//
// # Reference
//
// The developer-facing contract is documented in
// docs/architecture/corpus-proofing.md.
package corpusproof
