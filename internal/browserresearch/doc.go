// Package browserresearch executes bounded, fixture-backed public-source
// research for governed studies. It has no network transport: production
// navigation is intentionally a future injected adapter, while tests and
// cassettes use FixtureTransport.
package browserresearch
