# Workstream: Built-in Quota / Model / Auth Probes

## 1. 目标

让 built-in adapter 的 admin surface 尽可能反映真实本地状态，而不是只回显 binding config。

## 2. 价值

- operator 更快定位命令、cwd、auth、quota 问题
- `paperclip` 这类宿主不必重复写 probe glue code
- 控制面更适合作为桌面端或本地服务的 diagnostics 基础

## 3. 用户场景

- 为什么这台机器的 Claude / Codex 跑不起来
- 当前实际生效的是哪个模型
- 是没登录、没配置 API key，还是 quota 用完

## 4. 当前状态

当前 surface 已经到位：

- `CheckEnvironment()`
- `DetectModel()`
- `GetProfile()`
- `GetQuota()`
- `ConfigSchema()`

当前已实现的增强：

- `DetectModel()` 优先读取 binding config
- `codex` 会保守读取 `~/.codex/config.toml` / `config.json`
- `claude` 会保守读取 `~/.claude/settings.json` / `config.json` / `~/.claude.json`
- `cursor` 会保守读取 `~/.cursor/config.json` / `settings.json` / `argv.json`
- built-in `ConfigSchema()` 现在带稳定的 `Label` / `Hint` / `Default` / `Options` / `Group` / `Meta`
- built-in `CheckEnvironment()` 现在会报告 auth 与 config truth surface，而不只是 command/cwd：
  - `codex`: `OPENAI_API_KEY`、`auth.json`、auth email/plan/account hints、config model/provider
  - `claude`: `ANTHROPIC_API_KEY`、OAuth credentials、Bedrock mode/region、config model/base URL、Bedrock 下会被忽略的 binding model
  - `cursor`: `.cursor` / `CURSOR_HOME`、`CURSOR_API_KEY`、`OPENAI_API_KEY`、CLI auth user metadata、config model
- built-in probe 路径现在尊重本地 adapter home/config override：
  - `codex`: `CODEX_HOME`
  - `claude`: `CLAUDE_CONFIG_DIR`
  - `cursor`: `CURSOR_HOME`
- 宿主使用 profile option 作为高层配置入口：
  - `WithNativeProfile()` 复用原生共享 profile
  - `WithDedicatedProfile(dir)` 使用宿主专用 profile
  - `WithCloneProfile(dir, opts)` 从 native profile 派生专用 profile
  - `WithCloneProfileFrom(src, dst, opts)` 从指定源 profile 派生专用 profile
  - 显式写在 `CommonConfig.Env` 里的 adapter-specific env 仍然优先于 profile option
- built-in 本地 CLI 凭证接管规则已经和 `paperclip` 对齐：
  - `codex`: `OPENAI_API_KEY` 优先，否则接管本地 `auth.json`
  - `claude`: `ANTHROPIC_API_KEY` / Claude OAuth / Bedrock env；Bedrock 模式下只接受 Bedrock-native model id
  - `cursor`: `CURSOR_API_KEY` 或 `OPENAI_API_KEY` 进入 API mode，否则回退到本地 `agent login`
- built-in `GetQuota()` 现在不再全部是占位：
  - `codex`: 本地 `auth.json` 可用时，轮询 WHAM usage endpoint
  - `claude`: 本地 OAuth credentials 可用时，轮询 Anthropic usage endpoint
  - `cursor`: 继续诚实返回 unavailable

仍未实现的部分：

- `cursor` 的稳定 quota probe
- CLI-level hello/install probes and richer provider-specific auth diagnostics

## 4.1 当前可直接使用的合同

宿主现在可以依赖下面这些稳定用法：

### `ConfigSchema()`

- 直接渲染 `Fields`
- 使用 `Group` 做 UI 分组
- 使用 `Default` 做初始值提示
- 使用 `Options` 渲染下拉框
- 使用 `Meta` 读取 adapter-specific 风险或展示提示

### `CheckEnvironment()`

- 优先看 `Status`
- 再看 `Checks[*].Code`
- `Message` 用于直接展示
- `Detail` 用于展示路径、模型名、provider、base URL 等事实
- `Hint` 用于指导 operator 下一步修复动作

这意味着宿主不需要重新 hardcode 一份 “Codex / Claude / Cursor 本地诊断规则”。

### `GetProfile()`

- 返回 built-in adapter 的真实生效 profile 目录，而不是只回显原始配置输入
- `codex` 在未显式设置时会返回 SDK-managed `CODEX_HOME`
- `claude` / `cursor` 会返回最终生效的 `CLAUDE_CONFIG_DIR` / `CURSOR_HOME`
- `Source` 会区分：
  - `binding_env`
  - `profile_option`
  - `process_env`
  - `default`
  - `managed`
- `Managed=true` 目前只会出现在 `codex`

这让宿主可以直接缓存、展示或调试“这次到底会用哪个本地 profile”，而不必复刻各 adapter 的优先级判断。

### Profile Options

宿主优先提供：

- `WithNativeProfile()`
- `WithDedicatedProfile(dir)`
- `WithCloneProfile(dir, opts)`
- `WithCloneProfileFrom(src, dst, opts)`

而不是强制自己拼：

- `CODEX_HOME`
- `CLAUDE_CONFIG_DIR`
- `CURSOR_HOME`

固定优先级是：

1. `CommonConfig.Env` 中显式声明的 adapter-specific env
2. profile option
3. 进程环境与 adapter 本地默认路径
4. adapter 自己的 fallback，例如 `codex` 的 managed `CODEX_HOME`

注意：`GetProfile()` 返回的是“最终生效目录”，不是“原始配置输入”。这对 `codex` 尤其重要，因为没有显式 profile 输入时，执行面真正使用的是 managed `CODEX_HOME`。

## 5. 下一步

- `codex`: 必要时补 CLI hello probe 与更细的 auth-required 分类
- `claude`: 必要时补 login-required / install probe；当前本地 auth takeover 规则已对齐
- `cursor`: 评估是否存在稳定 quota/source truth，再决定是否补 quota probe

## 6. 边界

- 不在 core 封装 provider SDK
- 不承诺所有 adapter 都支持 quota probe
- 不把 provider-specific client 搬成新的核心依赖

## 7. 验收标准

- built-in `CheckEnvironment()` 能区分 command、cwd、auth 问题
- `DetectModel()` 尽量接近真实生效模型
- built-in `ConfigSchema()` 足够让宿主直接渲染基础设置界面，而不必自己猜字段分组和默认值
- 支持的 adapter 返回真实 quota windows；不支持的 adapter 明确 unavailable 而不是伪造数据
