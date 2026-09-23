# R050 — CodeBuddy formal tool-result error fields

Base: `42b112740233a0e9f35b8a265345043770b61e3f`. This is an independent formal-field projection defect; the actual catalog callback/deadline cause remains unknown.

Official CodeBuddy 2.157.0 `DeferExecuteTool.convertMcpResult` puts MCP `isError` in `rawResponse.is_error`; its stream-json transformer copies this to `tool_result._meta.rawResponse` while setting outer `is_error` from function-call status. A completed wrapper can therefore have outer false and inner true. The SDK previously ignored that exact inner field in capability, Transcript, and typed ToolResult projections.

The three projections now share one private reader. Boolean true in either `is_error` or exact `_meta.rawResponse.is_error` marks the tool result as an error. It does not change the enclosing Run failure or checkpoint policy. No content, error-code string, or recursively nested key supplies an error signal. Raw and original Transcript bodies remain complete; identical wrappers still produce one streamed result and retain both Transcript audit items.

Absent flags default false. Non-object `_meta`/`rawResponse` containers remain opaque. An explicitly recognized non-boolean flag follows the existing invalid-observation boundary: notice, no certified capability completion, and eventual Interrupted if unresolved. Transcript and typed ToolResult preserve the OR of available valid boolean signals, including true when the other flag is malformed. Malformed metadata alone does not become a global run failure.

The first test-build attempt had an incorrect map type assertion and is retained separately. After correcting only that test compilation error, the fixed formal frame failed all three intended assertions against unchanged production source. The valid red log is external `repairs/R050-T15-tool-result-error/owner/red-formal-frame-2.log`, SHA-256 `12e0ba6c71d8136ad104838c8aea215611fe329e292612fa4bf1f852223b3c9c`.

Regression coverage includes the exact frame, true/false OR combinations, absent/opaque/malformed fields, arbitrary nested/content/error-code negatives, duplicate wrappers, healthy terminal/checkpoint preservation, and public Run/Stream Raw/Transcript/capability/ToolResult delivery through the existing fake provider process. The public API is unchanged and no dependencies were added.

The final worker SHA must run the four original T15 commands unchanged plus scoped vet. Those results are recorded externally after the source commit; this handoff does not claim future checks or real-provider recovery. Root owns central AGENTS/CHANGELOG/godoc and independent acceptance.
