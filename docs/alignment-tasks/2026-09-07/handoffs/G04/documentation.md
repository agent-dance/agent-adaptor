# G04 provider/delegation integration and original-owner repairs

Canonical revision 22; unchanged accepted G03 base 926dbbf90a98d35416cdbbc6e376c7bbdc5da084.

| Worker | Accepted source SHA | Final worker checks |
|---|---|---|
| T14 | `e35680aed202a768430ae24f40f60fac35574a8b` | T14-V01=passed, T14-V02=passed, T14-V03-gated-live=passed, T14-V04-no-live-tag=passed |
| T15 | `9ef1ca39a3033700574e4b08cdccccd538d91283` | T15-V01=passed, T15-V02=passed, T15-V03=passed, T15-V04=passed, tagged-live-disabled=passed, untagged-env-enabled=passed, original-r018-fixed=passed, tagged-compile=passed, vet=passed, tagged-vet=passed |
| T16 | `4512e1e89ab88a9cd5260bc457d1defe59e415cc` | T16-V01=passed, T16-V02=passed, live-gate-disabled=passed |
| T17 | `eb374fe3fe15229a003117fc255f238dec862584` | T17-V01=passed, T17-TAG=passed, T17-RACE=passed, T17-VET=passed, T17-GATE-TAG=passed, T17-GATE-NOTAG=passed |
| T18 | `ca1330627a731078f7e913f5a5115bd54fd1c1e9` | T18-V01=passed, T18-V02=passed, T18-V03=passed |
| T19 | `075887499a1966ed4d08681bd837a4fcbfa1c650` | T19-V01=passed, T19-V02=passed, T19-X01=passed, T19-X02=passed |
| T32 | `9fd655c553551e8a0103743de99d37ba81ce0652` | T32-V01=passed, T32-V02=passed, T32-V03=passed |

R017 restores missing public Dedicated+WithTools cross-Agent cold-resume and exact Skill/Subagent live assertions. Disabled-tag/environment canaries prove gate behavior only. Two older Thread tool fixtures now supply the stable Revision already required by the public contract. All actual provider runs remain B06.

R018 repairs the publicly declared CodeBuddy Agents materialization path using the fixed official 2.137.1 loader. Exact resolved native names are distinct from safe .md filenames; SourcePath bytes stay native, with explicit name responsibility. Unmapped inline fields fail. Independent unchanged public case/Unicode/defaultKey/extension/native-source counterexamples turned from five failures to seven passing child cases. T21's original partial-wrapper success/error counterexamples also pass after the empty-Args, error-close ordering and identical typed ToolResult replay fixes; complete Raw/Transcript copies remain.

R019's supplemental T32 is executed by the original T12 bridge owner. Published AG-UI activity values own nested JSON containers and private tool structs, preserving types, nil/empty and prior wire behavior. T21 originally found both normal snapshot mutation and a real concurrent serialization race; those fixed inputs remain independent acceptance evidence. W05-R05/W09-R11 final implementation responsibility moves to T32, with original T12 source/report preserved historically. B04 has seven independent tasks, at most six executing at once; G04 must accept all seven. The validator retains complete barriers and pairwise scope checks, with explicit capacity, omitted-repair, overlap and lost-requirement controls.

R020 adds one exact T20 private test path to independently exercise existing post-OS-unlock close-failure seams; all original e2e commands remain and one focused race command is added. B05 QA reports are still phase evidence until they migrate to this replacement G04 and repeat original final commands.

Root integrated all current worker documentation into provider materialization, streaming/API, AG-UI snapshot semantics, CHANGELOG and the architecture audit notes. There is no B04 root/SPI golden, generated schema, public export or dependency change. The original six-group A2A/Local composition proof at2421fe4 remains historical because those production paths are unchanged; new fixed T21 counterexamples are replayed separately on this final committed candidate.

This committed fragment precedes the final G04 exact-SHA checks. The original validator/full Go suite/vet and original T21 counterexamples must pass on the resulting source commit before acceptance. Old G04 2421fe4 and all failed attempts, toolchain diagnostics, QA oracle calibrations and independent red evidence remain externally archived, never relabeled as new-source proof.

B05 final QA and B06 Linux/native Windows/authorized live remain open. No push, tag, release, paid call or native-platform certification is implied. Main unrelated DSH/session/probe work and the evidence module boundary remain preserved.
