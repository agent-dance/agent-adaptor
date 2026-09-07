# T20 new evidence coverage

Unless qualified as `TestAlignmentReleaseLifecycle`, names below begin with `TestAlignmentLifecycle`. Public Agent/Thread tests and the R020-authorized private-seam test are independent of owner fixtures. Passing an owner test is not counted here. Native platform/live requirements stay open for their other designated verifiers even when T20 passes.

| Requirement | New independent scenarios |
|---|---|
| W01-R01 | ResultOnly (Run/Stream, result-only, failure, native schema) |
| W01-R02 | ResultOnly/message-stop; ApprovalSchema (intermediate tool_use stop and actual same-stdin response JSON) |
| W01-R03 | ResidentSchemaPrewarm (two prompts, one actual PID) |
| W01-R04 | ResultOnly full Raw/Terminal/Transcript; TerminalCancellationRace five iterations, one core terminal |
| W02-R01 | ProfileColdResume; ProfileIdentityEncoding; ProfileTemporaryCleanup all four temporary selections |
| W02-R02 | ProfileUnsafeAndModes; ProfileColdResume seed-once/session bytes; ProfileOwnership private modes |
| W02-R03 | ProfileOwnership same-process and subprocess; ProfileIdentityEncoding; ProfileCrashRequiresRecovery |
| W02-R04 | ProfileCloseCleanupRetry; CloseDeadlineRetry; ProfileOwnership successor generation unchanged; `TestAlignmentReleaseLifecycle` lock-close/root-close failures with B held active |
| W02-R05 | ProfileColdResume credential hashes/carriers, successful old-token authentication before Close and same-token revocation after Close, owned projection cleanup, source unchanged |
| W02-R06 | RealCompatibilityGuards model/workspace/append; ProfileUnsafeAndModes mode/Revision; ProfileMCPModePreserved; ResidentSchemaPrewarm active record identity |
| W02-R07 | ProfileTemporaryCleanup Default/Native/CloneFrom/CloneNative; ProfileCloseCleanupRetry; CloseDeadlineRetry; `TestAlignmentReleaseLifecycle` post-unlock retry preserves all successor files/lock |
| W02-R08 | ProfileColdResume disk nonce, new Agent ResumeOnly; ProfileMissingSession reject/default single fallback and atomic archive |
| W04-R01 | RunStreamAuditEquivalence; PartialProviders; DeadlinePreservesAudit; StaticRejection; AdmissionBoundary |
| W04-R02 | RunStreamAuditEquivalence independently expected complete partial carrier fields (Transcript/Services maps and Terminal.Event included); StructuredAuditEquivalence complete success/Decode fields |
| W04-R03 | PartialProviders full record equality; UnhealthyCheckpoint nonzero/malformed/missing/terminal-nonzero |
| W04-R04 | ApprovalSchema deny/timeout; FinalAuthority lease/cleanup and cause; DeadlinePreservesAudit |
| W04-R05 | PartialProviders/claude; BootFailureBoundary/claude reads one actual prompt byte before failing, proving no replay after partial delivery |
| W04-R06 | PartialProviders/codebuddy; UnhealthyCheckpoint/codebuddy; BootFailureBoundary/codebuddy one safe retry |
| W04-R07 | PartialProviders/codex Run/Stream full public Raw/Transcript/Usage; DeadlinePreservesAudit; BootFailureBoundary/codex |
| W06-R01 | ApprovalSchema Question/PlanReview actual request + native schema + exact complete control wire (subtype/request_id/toolUseID/updatedInput, no synthetic fields) + terminal Decode |
| W06-R02 | ApprovalSchema Permission prompt fallback; native Question/PlanReview; PermissionWithoutSchema real allow Run/Stream; ResultOnly interactive stdin |
| W06-R03 | ApprovalSchema explicit per-Kind native/prompt selection; LegacyHITLMatrix nil pointers × explicit/unset × WorksWithHITL true/false, ordinary policy first, resolved source/resource/Driver/approval counts |
| W06-R04 | ApprovalSchema answer/deny/timeout and Thread+WithSpawn; ResultOnly invalid output; ResidentSchemaPrewarm |
| W06-R05 | ResultOnly/ApprovalSchema same terminal JSON, full Raw, Decode; ResidentSchemaPrewarm checkpoint; UnhealthyCheckpoint |
| W09-R14 | FinalAuthority driver-only/source × cleanup/lease barriers; AdmissionBoundary; StaticRejection; every streamed scenario's strict lifecycle envelope |

R007/R020: `TestAlignmentReleaseLifecycle` is new independent private-seam evidence, additional to the public cleanup/successor scenarios. Actual kernel unlock is proven by B acquiring and publishing active state before A retries; injected lock-close/root-close failures remain observable; repeated A release cannot change B state/owner/seed/lock/session bytes or admit a third contender. The prior T04 test is not counted. Windows no-delete-sharing/ACL is not run here. R011 checks actual MCP file modes and same active store record across rich/schema/prewarm, plus genuine configuration drift. R010 distinguishes rejected admission from an admitted pre-Driver resource failure. R006 asserts no terminal while cleanup is blocked and matching final RunError after release.

All Events/Result, cleanup-entry/release/done, Close and private Claim operations have independent local watchdogs and failure-path barrier release. Watchdogs are test bounds, not product timeout policy.
