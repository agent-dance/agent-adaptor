# T21-F03 / R019 — resolved in T32

The supplied T21 logs at 8b52ce06 and b8d9ce6 identify shared published tool-call slices: standard AG-UI stream serialization races with a later tool status write, and retained frames change after delivery. The originals are preserved under evidence without editing T21 input or assertions.

Before production changes, the independent public-boundary fixtures reproduced retained JSON mutation and nested consumer edits leaking back into translator state. A separate Events race run reproduced DATA RACE with ordinary json.Marshal. The first test compilation used an incorrect fixture type name; that compiler log is retained separately and is not counted as a reproduction or executed-test pass.

The repair is confined to `bridges/agui/subagent.go` publication copies. It recursively isolates the complete tool-call list and its Args/Result/Error JSON containers, along with activity-level Result/Error and flush error values. No input/consumer serialization lock, shallow-only copy, wire change, skip, reduced required command, public API or new dependency is used. Final committed-SHA command results and hashes are recorded in result.json and evidence. G04 and T21 independent acceptance remain separate from this worker delivery.
