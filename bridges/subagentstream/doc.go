// Package subagentstream preserves a Runner's single authoritative Event
// stream. Merge is transparent and requires proof of a run service attachment;
// delegation facts must enter the unique core sink before consumer backpressure.
// It never injects bus mirrors, reassigns EventMeta, or synthesizes terminals.
package subagentstream
