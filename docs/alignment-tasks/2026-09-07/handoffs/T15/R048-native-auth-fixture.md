# R048：CodeBuddy 原生登录副本夹具

基线为 `6934126c11bf886eaedbcbdb9aace18d64806a97`。本补充仅修改 T15 的测试夹具与本局部交接文档，不增加 SDK 认证 API，不改变生产认证、模型、prompt、live oracle、预算或原 T15 验证命令。

## 问题与实现

旧 `isolatedConfigDir` 只把 `.credentials.json` / `credentials.json` 复制到 `CODEBUDDY_CONFIG_DIR`，另设的空 HOME 没有官方原生 session 文件。现在显式设置 `CODEBUDDY_NATIVE_AUTH_FILE_SOURCE` 时，夹具将完整 opaque JSON 原字节复制到私有 HOME 下的官方位置：

| 平台 | HOME 下相对路径 |
| --- | --- |
| macOS | `Library/Application Support/CodeBuddyExtension/Data/Public/auth/Tencent-Cloud.coding-copilot.info` |
| Windows | `AppData/Local/CodeBuddyExtension/Data/Public/auth/Tencent-Cloud.coding-copilot.info` |
| Linux | `.local/share/CodeBuddyExtension/Data/Public/auth/Tencent-Cloud.coding-copilot.info` |

`newLiveAgent`、真实冷续接测试的共享配置准备函数、实际 conformance 的配置准备函数统一使用该夹具；两代冷续接 Agent 共享同一私有 HOME。HOME、USERPROFILE、配置与临时目录全部指向本次夹具。业务 profile、规则、MCP、历史、额外认证文件均不从来源复制。关闭 Agent 后通过持有的目录句柄清理副本与本次运行产物；源文件不修改。

来源必须是绝对、clean 路径，父链及末文件不得为 symlink/reparse。逐层持有 root 并比较读取前后的身份；邻接 `.logged-out` 即使为 dangling symlink 也明确拒绝。文件必须是非空 JSON object，最大 8 MiB；这只是夹具输入验证，不解析、解密或推断凭据有效性。读取、写入及关闭的错误保留，包括 Close 次因。macOS 的 `/var` 等路径别名需由操作者显式提供其真实路径，夹具不自动跟随来源 symlink。

双重 live 门通过后，`requireCodeBuddyCLI` 在任何 `--version` / `--help` 进程之前执行同一只读输入验证；执行准备再次验证。缺失、无效或不安全的显式来源不能回退成无认证成功。禁用 live 门先返回，不读取 seed。

原生模式拒绝非空 `CODEBUDDY_AUTH_TOKEN`、生效的 `CODEBUDDY_API_KEY`、已确认的 route/header/product override，以及 `CODEBUDDY_CREDENTIALS_IN_MEMORY=1`。API key 仅在 `CODEBUDDY_API_KEY_DISABLED` 为空时生效；官方 JS 的任意非空 disabled 值（包括 `0`、`false`）均禁用 API key，其原字节保留。原生模式未提供 disabled 时设置 `1`。原先明确的环境 token 模式与 `CODEBUDDY_CONFIG_DIR_SOURCE` 的两种允许 credential 文件继续可用；空 legacy 来源只有在明确有效环境 token 存在时才可接受。

POSIX 对象创建时采用 HOME/目录 0700、文件 0600。Windows 复用仓库已有私有对象创建方法的窄测试实现：创建时即使用当前 owner/SYSTEM 的 protected DACL，验证 owner、ACL、reparse/link 属性，并在生命周期持有不允许 delete-sharing 的目录句柄。写入凭据前即保护文件；凭据文件句柄在启动 provider 前关闭，允许官方刷新时替换文件。Windows 合同测试在普通测试构建中，不依赖 live tag。

## 证据与限制

外部证据根为 `docs/alignment-execution/2026-09-16/live-repair/repairs/R048-T15-native-auth-fixture/owner`，不纳入本提交。先在旧 wrapper 上保存了真实可达的 red：私有测试可执行文件读取不到原生 session 且 HOME/USERPROFILE 不一致（exit 83）；完整 red patch 和日志保留。随后单独执行的 `TestCodeBuddyNativeAuthFixture...` 合同组覆盖三种实际配置调用点、两代冷续接配置、opaque bytes、刷新替换、源不变、清理、父链/登出标记拒绝、环境模式与 API disabled 语义。缺失 seed 控制通过子测试进程进入实际外层 `requireCodeBuddyCLI`，断言失败发生在任何 CLI canary 前。该负例只在子测试中打开门，PATH 固定为测试二进制；外部 runner 的三门仍为 0。

提交前 `green-complete-fixtures`、`green-reviewed-fixtures`、`green-callsite-fixtures` 均通过；Windows 带 live tag 测试二进制交叉编译通过。这些只证明离线夹具合同及编译，不是原生 Windows 权限验收或真实模型证据。最初 unused import 的构建失败也保留，未覆盖日志。

root 单次正式原生认证诊断的成功，只证明完整原生副本与该模型在该次调用可用；不得倒填旧 cold/catalog 或 API-key 401 的原因。本 worker 未读取真实凭据，未运行原 CLI/付费调用。原生 Windows、合流新 SHA 的全量 G05、原 T28 live 矩阵及 G06 由 root 独立验收。

本提交冻结时，原四检查及补充 race/vet 尚待在真实提交 SHA 上执行；最终结果与身份、完整 diff、散列仅写外部报告，不把将来结果预写为通过：

```text
go test -count=1 ./codebuddy
go test -race -count=5 ./codebuddy -run TestAlignment
go test -count=1 ./internal/profileagents
go test -race -count=10 ./internal/profileagents
go vet ./codebuddy ./internal/profileagents
```

固定 Go 1.27.1，GOPROXY/GOWORK off，私有 HOME，`AGENT_ADAPTOR_LIVE_CONFORMANCE`、`AGENT_ADAPTOR_E2E`、`UPDATE_API_GOLDEN` 均为 0，真实 CLI 名称前置 canary。新合同测试名不匹配原付费选择器 `TestAlignmentLive` / `TestCodeBuddyDriverConformance`。

## 中央记录建议文字（由 root 整合）

使用文档：CodeBuddy live 测试可通过 `CODEBUDDY_NATIVE_AUTH_FILE_SOURCE` 显式提供官方原生登录 session 文件；夹具保留完整字节，在当前平台官方路径创建受保护的私有副本，并在 Agent 关闭后删除。来源必须为无链接父链的绝对路径，且无 `.logged-out` 标记；不能同时设置生效的环境 API key/token 或 route/product override。原有显式环境认证仍可用。缺失/不安全来源在 CLI 启动前明确失败，禁用 live 门不读取来源。

CHANGELOG：修复 CodeBuddy live 测试在隔离 HOME 后未传递官方原生登录 session 的夹具缺陷；增加显式原生认证文件来源、跨平台受保护副本、统一三分支准备与清理合同，保留环境认证模式。新增离线 fake 与普通 Windows 权限合同测试，不改变公共 SDK 认证或真实模型断言；真实 live 与原生平台验收仍独立执行。
