// Package runtime manages exact-source review runtime instances.
//
// It intentionally owns no ambient ports, processes, or workspaces. Those
// boundaries are supplied as narrow interfaces so the host provider can be
// tested without a live service and future contained providers cannot pretend
// to provide host enforcement.
package runtime
