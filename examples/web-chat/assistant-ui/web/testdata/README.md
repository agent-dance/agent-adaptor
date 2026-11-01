# Real Codex history regression

`codex-history.json` is a sanitized real Codex conversation: three user turns,
four assistant records, two
tool results (one failed), and an empty reasoning record. Message/tool IDs are
replaced consistently; prompts, replies, commands and output are sanitized.
Absolute paths, provider identities and unrelated metadata are removed. Roles,
record order, content emptiness, tool associations and failure status are kept.

The empty reasoning record intentionally represents history recorded before
the lazy-opener correction. assistant-ui 0.1.1 drops it on round-trip, with
either showThinking setting. Non-empty reasoning is separately tested with
showThinking=false. The server verifier must compare the remaining mandatory
user/assistant/tool identities, and accept both presentations.
