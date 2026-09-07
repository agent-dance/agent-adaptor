# CodeBuddy Agent loader 来源与物化边界

只读本地正式 npm 包 `@tencent-ai/codebuddy-code@2.137.1`。文件 `/opt/homebrew/lib/node_modules/@tencent-ai/codebuddy-code/dist/codebuddy.js`，22852323 bytes，SHA-256 `7fa1c542cca9eebe9db1f6e70fe50c759bdd87aa2fcacda3b7007ec408b4958f`。未执行 CLI，未读认证文件或用户 profile。此静态来源证明格式；实际 CLI 采用由禁用的 live 用例留给 B06。

以下位置均为零基偏移；字符偏移按 UTF-8 解码后的 Unicode 字符计数，byte offset 对原文件计数，二者不可混用。完整短片段及来源 hash 在 `evidence/attempt-3/official-loader-extract.json`。

| 正式符号 | Unicode 字符偏移 | UTF-8 byte offset | 已核实行为 |
|---|---:|---:|---|
| `CustomAgentsProductProvider.loadCustomAgents` | 10688002 | 10754170 | 扫描 project 与 user agents。 |
| `CustomAgentsProductProvider.scanAgentsDirectory` | 10689294 | 10755462 | 只选择 `.md` 后交 parseAgentFile。 |
| `CustomAgentsProductProvider.parseAgentFile` | 10689556 | 10755724 | YAML frontmatter 与 Markdown body 转 native agent。 |
| `PathUtils.getHomeDir` | 13918573 | 13991815 | 非空 CODEBUDDY_CONFIG_DIR 优先；否则 homedir/.codebuddy。 |
| `PathUtils.getHomeAgentsDir` | 13918705 | 13991947 | join(getHomeDir(), "agents")。 |
| `SessionStore.getStorageDir` | 11335314 | 11403626 | 使用 getHomeProjectDir。 |
| `SessionStore.getSessionFilePath` | 11340772 | 11409084 | 根会话 `<id>.jsonl`；subagent 使用显式独立目录。 |
| `PathUtils.getHomeProjectDir` | 13918074 | 13991316 | projects 下压缩工作目录。 |
| `PathUtils.getHomeProjectsDir` | 13918318 | 13991560 | join(getHomeDir(), "projects")。 |

`parseAgentFile` 的关键原式为 `eh=ed.name||eg`、`em=ed.description||eh`、`instructions: substituteLoadTimeVariables(eu.content.trim(),eA.fullPath)`。loader 会添加来源描述标签；不能把描述字节或文件存在误当执行事实。

| SDK 已有声明 | CodeBuddy frontmatter / body | 决策 |
|---|---|---|
| RuntimeName / Key | name 与独立安全 `.md` 文件名 | name 保留 resolved RuntimeName 的 Unicode/大小写/扩展及默认 Key；简单可移植名保持原文件名，其他使用 agent~SHA256；文件编码不改变 catalog。 |
| Description / Instructions | description / Markdown 正文 | 最小 portable core；既有空值默认说明与 trim 保持。 |
| Model | model | loader `ed.model?.trim()`，生成 models/declaredModel。 |
| ReasoningEffort | effort | loader normalizeReasoningEffort；minimal/low/medium/high/xhigh/max，未知值 Go error。 |
| PermissionMode | permissionMode | loader 直接读取该字段；保留既有字符串词汇。 |
| ToolPolicy Allow / Deny | tools / disallowedTools | loader parseListField；安全引用列表。 |
| Skills | skills | loader parseListField。 |
| MCPServers 名称列表 | mcpServers | 独立 parseMcpServers 接受 string 数组；不生成 inline server 对象。 |
| SandboxMode / Hooks / Native | 未映射 inline 扩展 | 明确 error；不静默丢弃，也不把 provider 原始 hooks 当 SDK HookSpec 的等价序列。 |
| SourcePath | 原生文件原字节 | 保留已有 native escape；扩展字段应在明确原生 source 中由 provider 解释。 |

CodeBuddy renderer 局部化在 `internal/profileagents/codebuddy.go`，`agents.go` 仅增加 CodeBuddy layout/render/warning 分派；没有修改 Claude/Cursor/Codex 的渲染路径。没有新公共字段、golden 或依赖。

公开零 CLI 红例：533398e 的 `adaptor.New(codebuddy.Driver(Config{Command: executable, Env: private HOME/USERPROFILE + CLI canary}), WithProfile(Dedicated(privateRoot)), WithProfileResources(Skills+Agents)).SyncProfile(ctx)` 返回 `profile agents are unsupported by driver "codebuddy"`，canary touches=0。原源码快照与红日志保留于 `evidence/attempt-3/subagent-materialization-canary.go.txt` 和 `materialization-canary-before/`。实际 fake 子进程在修复后读取生成的两个资源文件，再输出正式 partial/wrapper/result，公开 canonical ProviderProtocol Skill/Subagent 事实与 Tool/Transcript 同 ID 完成。

SessionStore 的文件证据只用于 live oracle：相同 session ID 必须指向同一实际 JSONL 历史文件并含第一轮真实 tool 随机 nonce。第二 Agent 的 prompt/callback/store 不补回 nonce，不能以 SPI Resume 状态回显替代历史召回。

独立复核修正：53dbc0d 原代码复用 shared agentName/runtimeFileName 的小写/ASCII化/扩展处理，导致 Unicode/case/default key 与 catalog 不一致，.json/.txt 不被正式 `.md` scanner 读取。原 C01 fixture SHA256 `9df5cd2c44972d73aa317ae34f38c68bfe1bf480d92138a19ad5cec50a3ae51e` 在 owner 原样 overlay 重现 2 pass / 5 子项 fail。修复只增加 CodeBuddy name/file 分支；旧 provider helper 不动。SourcePath 保持原始内容而目标统一 .md。

SourcePath 原生身份责任：helper 不解析/重写原生 frontmatter；显式 name 须与 resolved RuntimeName 对应。未写 name 时官方退到文件 basename；编码文件名场景不能依赖这个 fallback 获得原 catalog identity。`.md` 物化与字节保留仅证明 loader 可读，不宣称任意原生文件均能关联 canonical key。编码前缀 `agent~` 位于简单名称允许字符集之外，caller 同前缀名称也会编码，避免文件命名空间冲突。
