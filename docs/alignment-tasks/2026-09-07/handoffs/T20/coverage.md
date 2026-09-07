# T20 new evidence coverage

All names below begin with `TestAlignmentLifecycle`. They are new independent tests at the public Agent/Thread boundary. Passing an owner test is not counted here. Native platform/live requirements stay open for their other designated verifiers even when T20 passes.

| Requirement | New independent scenarios |
|---|---|
| W01-R01 | ResultOnly (Run/Stream, result-only, failure, native schema) |
| W01-R02 | ResultOnly/message-stop; ApprovalSchema (intermediate tool_use stop and actual same-stdin response JSON) |
| W01-R03 | ResidentSchemaPrewarm (two prompts, one actual PID) |
| W01-R04 | ResultOnly full Raw/Terminal/Transcript; TerminalCancellationRace five iterations, one core terminal |
| W02-R01 | ProfileColdResume; ProfileIdentityEncoding; ProfileTemporaryCleanup all four temporary selections |
| W02-R02 | ProfileUnsafeAndModes; ProfileColdResume seed-once/session bytes; ProfileOwnership private modes |
| W02-R03 | ProfileOwnership same-process and subprocess; ProfileIdentityEncoding; ProfileCrashRequiresRecovery |
| W02-R04 | ProfileCloseCleanupRetry; CloseDeadlineRetry; ProfileOwnership successor generation unchanged |
| W02-R05 | ProfileColdResume credential hashes/carriers, old endpoint rejection, owned projection cleanup, source unchanged |
| W02-R06 | RealCompatibilityGuards model/workspace/append; ProfileUnsafeAndModes mode/Revision; ProfileMCPModePreserved; ResidentSchemaPrewarm active record identity |
| W02-R07 | ProfileTemporaryCleanup Default/Native/CloneFrom/CloneNative; ProfileCloseCleanupRetry; CloseDeadlineRetry |
| W02-R08 | ProfileColdResume disk nonce, new Agent ResumeOnly; ProfileMissingSession reject/default single fallback and atomic archive |
| W04-R01 | RunStreamAuditEquivalence; PartialProviders; DeadlinePreservesAudit; StaticRejection; AdmissionBoundary |
| W04-R02 | RunStreamAuditEquivalence complete partial carrier fields; StructuredAuditEquivalence complete success/Decode fields |
| W04-R03 | PartialProviders full record equality; UnhealthyCheckpoint nonzero/malformed/missing/terminal-nonzero |
| W04-R04 | ApprovalSchema deny/timeout; FinalAuthority lease/cleanup and cause; DeadlinePreservesAudit |
| W04-R05 | PartialProviders/claude; BootFailureBoundary/claude reads one actual prompt byte before failing, proving no replay after partial delivery |
| W04-R06 | PartialProviders/codebuddy; UnhealthyCheckpoint/codebuddy; BootFailureBoundary/codebuddy one safe retry |
| W04-R07 | PartialProviders/codex Run/Stream full public Raw/Transcript/Usage; DeadlinePreservesAudit; BootFailureBoundary/codex |
| W06-R01 | ApprovalSchema Question/PlanReview actual request + native schema + control answer + terminal Decode |
| W06-R02 | ApprovalSchema Permission prompt fallback; native Question/PlanReview; ResultOnly interactive stdin |
| W06-R03 | ApprovalSchema explicit per-Kind native/prompt selection; StructuredAuditEquivalence nil legacy matrix success |
| W06-R04 | ApprovalSchema answer/deny/timeout and Thread+WithSpawn; ResultOnly invalid output; ResidentSchemaPrewarm |
| W06-R05 | ResultOnly/ApprovalSchema same terminal JSON, full Raw, Decode; ResidentSchemaPrewarm checkpoint; UnhealthyCheckpoint |
| W09-R14 | FinalAuthority driver-only/source × cleanup/lease barriers; AdmissionBoundary; StaticRejection; every streamed scenario's strict lifecycle envelope |

R007 supplemental boundary: public cleanup-failure retention and normal successor safety are new. Exact post-unlock OS-handle failure remains referenced T04 evidence, not new T20/native evidence. Windows no-delete-sharing/ACL is not run here. R011 checks actual MCP file modes and same active store record across rich/schema/prewarm, plus genuine configuration drift. R010 distinguishes rejected admission from an admitted pre-Driver resource failure. R006 asserts no terminal while cleanup is blocked and matching final RunError after release.
