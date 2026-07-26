// Package driver defines the SPI (service provider interface) implemented by
// agent CLI integrations. It is the package for driver authors: anyone who
// wants to plug a new coding agent into the SDK implements [Driver] (plus any
// of the optional capability interfaces) against the request/response/event
// types declared here.
//
// Application code that consumes the SDK does not need to import this
// package. The root package github.com/agent-dance/agent-adaptor re-exports
// every type in this package under its historical name via type aliases (for
// example agentadaptor.DriverAdapter is an alias for [Driver]), so hosts keep
// working against the root package only.
//
// The dependency direction is one-way: the root package imports driver,
// never the reverse.
package driver
