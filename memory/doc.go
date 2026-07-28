// Package memory provides a concurrency-safe, in-memory implementation of
// threadstore.Store.
//
// A [Store] is suitable for tests, local tools, and single-process hosts. Its
// records and leases disappear when the process exits, so hosts that require
// durable or cross-process thread coordination should provide another
// threadstore.Store implementation.
package memory
