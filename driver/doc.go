// Package driver defines the SPI (service provider interface) implemented by
// agent CLI integrations. It is the package for driver authors: anyone who
// wants to plug a new coding agent into the SDK implements [Driver] (plus any
// of the optional capability interfaces) against the request/response/event
// types declared here.
//
// Application code that consumes the SDK normally imports only the root
// adaptor package and provider packages. This package is intentionally the
// extension-author boundary. The root package provides a convenience alias
// for [Driver] so an Agent can expose its required construction dependency,
// while the canonical SPI and every optional capability contract remain here.
//
// The dependency direction is one-way: the root package imports driver,
// never the reverse. Provider packages may import driver to implement this
// SPI; driver must not import the root package, provider packages, bridges, or
// internal implementation packages.
package driver
