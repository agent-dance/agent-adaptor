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
//
// Observations use the public capability and todo leaf vocabularies. Drivers
// MUST derive facts from their official protocols and the current resolved
// catalog, never from tool-name guesses, prompt text or resource declarations.
// A configured capability is not proof of use. ObservationCapabilities reports
// each transport truthfully; a zero field means unavailable. Request.Observation
// is demand, not evidence and not a second execution mode.
//
// Drivers serialize tracker/table transitions together with EmitStream so a
// terminal cannot overtake its start. They keep invocation and tool identifiers
// stable within run/scope, preserve explicit parent coordinates, close every
// opened lifecycle before their provider terminal, and never complete an open
// invocation merely because the overall run succeeded. Unknown catalog names,
// ambiguous aliases and unconfirmed todo updates produce no invented facts.
//
// Core owns the public run envelope for every admitted execution, regardless
// of transport. The Driver terminal remains an official provider fact; core
// publishes its one final terminal only after Result/error and teardown settle.
// Drivers retain original terminal JSON in Response.Raw.Terminal, including on
// failed executions with a partial Response. Observer failures do not change
// the Driver response, approvals or checkpoint validity.
package driver
