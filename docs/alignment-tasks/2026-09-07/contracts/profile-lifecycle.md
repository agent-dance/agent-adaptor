# Dedicated hosted-tool profile 生命周期合同

合同 ID：C01 / W02。版本：`hosted-profile/v1`。实施 owner：T04；独立验证：T20、T26–T30；冻结 owner：G00。本文件冻结实施决定，不声称实现、Windows 或真实 CLI 验收已通过。

依据为 seed `bc0d421f9c0b1e80e529d1e843fe5d8396eab02f` 的 AGENTS §§5、6、10、12、14，及对齐方案 W02。internal 仅通过固定对象 `ca571fbed8454e482560b481a3aa6853e9d174df` 的 `tools.go`、`tools_contract_test.go`、`e2e/tools_test.go` 读取；其 sibling 持久化提供故障证据，不提供本合同的所有权证明。

## 1. 冻结的外部行为

1. 同时满足非空构造期 `WithTools`、内置 Driver 支持 hosted profile、显式 `profile.Dedicated(dir)` 时，执行 clone 持久。环境变量恰好指向专用目录、`profile.Default()`、`profile.Native()`、`profile.CloneNative`、`profile.CloneFrom` 均不自动升级为持久 hosted clone。
2. Dedicated 的 source 与执行 clone 分离。首次只种入 settings、MCP、skills，并使用既有 `AuthLink` 策略；provider 创建的 transcript、session index、项目目录留在执行 clone。`Agent.Close` 保留持久 clone，临时 clone 仍删除。
3. 一组 Driver、canonical source、完整 identity 对应一个持久执行目录。Agent 持有 claim 后，另一 Agent（包括同一 OS 进程内的另一个 Agent）使用该目录立即得到 `profile.ErrInUse`；不同 identity 得到不同目录。Thread key 不参与目录命名。无 identity 是明确的全空 identity，两个这样的 Agent 仍互斥。
4. 正常 Close 后，下一 Agent 可以接力已有文件；gateway URL、bearer token 及其随机环境变量载体重新分配。持久目录不赋予旧 token 有效性，也不放宽任何 Thread 配置或 checkpoint 检查。
5. 异常退出释放 OS 锁，不证明 provider 子进程已退出。上一 generation 标记 `active` 时，新 Agent 返回 `profile.ErrRecoveryRequired`，不发送 prompt、不改 Thread store、不重放、不清除 marker。§6 定义离线恢复。此安全边界经协调者确认，不宣称无条件自动崩溃恢复。
6. 旧版本已经删除的 transcript 不能从 resume ID 重建。保留既有 continue-or-start 的一次安全 resume-reject fallback；`ResumeOnly()` 保持拒绝语义，宿主 key 逐字保留。

唯一公共新增声明落在 `profile/errors.go`，使用真实 sentinel，不做 root alias：

```go
var (
    ErrInUse = errors.New("profile: hosted profile is in use")
    ErrUnsafe = errors.New("profile: unsafe hosted profile")
    ErrRecoveryRequired = errors.New("profile: hosted profile requires offline recovery")
    ErrUnsupportedFilesystem = errors.New("profile: hosted profile filesystem is unsupported")
)
```

冲突不是业务失败。Run/Stream 按现有启动前基础设施错误路径包装，`errors.Is` 保留上述 sentinel、`context.Canceled`、`context.DeadlineExceeded` 及实际 `*os.PathError`；不新增 `Result.Failure`、执行入口、root `With*` 或 Driver SPI 字段。错误可含安全的 namespace hash、阶段和路径，禁止包含 token、env value、原始 identity、manifest 全文或 provider session 正文。

## 2. 基线实况与职责边界

现有顺序是 `registerRun` → `prepareHostedToolProfile` → `claimHostedToolProfile` → runtime attachment / resolved request → Thread fingerprint → `driver.Run`。四个 Driver 在 Run 内物化 provider 资源；不是 core 调用 `Agent.SyncProfile`。当前 prepare 对所有选择 `MkdirTemp`，claim 会再次 `GetProfile` 并计算物化指纹；Close 先尝试回收进程、drain、再次回收晚启动进程，随后去 MCP 投影、删除全部临时 clone、关 gateway。

T04 保留上述唯一执行管线和既有准入边界。`Inspect`、`ProfileState` 不获取 claim、不创建 sibling、不启动 gateway；显式 `SyncProfile` 继续针对调用方的 source desired resources，不成为运行期 hosted clone 的第二个执行入口。显式 SyncProfile 的宿主写入属于原接口授权，不能被描述为 WithTools 对 source 的隐式写入。

`internal/profilestate.AcquireLock` 的 O_EXCL 文件、25ms retry 与 `StaleAfter` 服务一次资源写入；按年龄删锁不能证明 Agent 或常驻 writer 已退出。本合同的专用所有权锁不复用该锁，不改变其公共或内部合同，也不以 Thread lease 替代目录所有权。锁顺序固定为 Agent claim 协调 → hosted claim OS 锁 → 短期资源锁；禁止资源锁反向获取 hosted claim。

## 3. 目录、编码及 marker

### 3.1 canonical source 与 key

显式 Dedicated source 必须是已存在的目录。hosted 路径不得为解析 source 调用会执行 `ensureDedicatedProfile` 的 `GetProfile`；使用既有路径展开规则，只读打开并 canonicalize 显式 selection。不存在时保留 `os.ErrNotExist`，不静默创建 source。selection 优先于 Driver Config/env 的既有合同不变。

source 输入可以经合法路径别名到达同一目录，canonicalize 后只使用实际目录：Unix 使用绝对路径、`EvalSymlinks` 与打开后文件身份；Windows 使用目录句柄的 `GetFinalPathNameByHandle`，去掉等价 Win32 扩展前缀并使用返回的实际大小写，拒绝远程卷。source 的 dev/inode（Unix）或 volume serial/file index（Windows）写入 source identity，防止同路径被删后换成另一个目录。filesystem identity 只做本机目录归属验证，不充当跨机器 Thread fingerprint。

key 输入为固定顺序 7 个字符串：

```text
"adaptor/hosted-profile-key/v1", driverType, canonicalSource,
identity.ID, identity.TenantID, identity.ProfileID, identity.Name
```

逐字段使用 `uint64` big endian UTF-8/Go string **字节长度** + 原始字节编码，接着 SHA-256，得到 64 位小写 hex `keyHash`。不 trim identity、不做 Unicode 归一化、不拼接分隔符；全空 identity 与字段换位都明确定义。hash 输入中不含 Agent 指针、PID、随机 generation、Thread key、模型或 endpoint。完整 key 输入的摘要与 manifest 身份必须一致；摘要相同但 manifest 身份不同按 `ErrUnsafe` 拒绝，不把 hash 当作无条件采用目录的授权。

目录布局固定为：

```text
<canonicalSource>.hosted-tools/
  namespace.json
  v1/
    <keyHash>/
      owner.json
      owner.lock
      state.json
      seed.json
      profile/                 # 唯一 provider 执行目录
      .seed-<generation>/       # 仅初始化中存在
```

所有持久 SDK 管理路径必须在 canonical source 的 sibling 内，不能等于 source 或位于 source 内；使用路径组件比较，不用裸字符串前缀。目录名不含宿主 Thread key。`owner.lock` 永久保留且不能 rename、truncate 或 unlink；否则旧 inode 仍被锁时新 inode 会产生第二 owner。profile 目录绝不传给 `removeHostedToolProfileDir`，也不扩宽该临时删除函数的白名单。

### 3.2 精确 JSON 记录

三个 marker 都用严格 JSON 对象解析：拒绝重复/未知 key、缺失 key、非法 UTF-8、尾部第二个 JSON 值和超过 64 KiB 的记录。所有 hash 为 64 位小写 hex，generation 为 `crypto/rand` 16 字节的 32 位小写 hex。marker 不是 secret store。

`namespace.json` 的封闭字段为 `format:"agent-adaptor/hosted-profile-namespace"`、`version:1`、`source_dir:string`、`source_id:string`。`owner.json` 的封闭字段为 `format:"agent-adaptor/hosted-profile-owner"`、`version:1`、`key_hash:string`、`driver_type:string`、`source_dir:string`、`source_id:string`、`identity_hash:string`。`identity_hash` 为同样长度前缀编码四个 identity 字段的 SHA-256，避免在 marker 存原始 identity。

`state.json` 的封闭字段为 `version:1`、`phase:"unseeded"|"ready"|"active"`、`generation:string`。unseeded/ready 的 generation 必须为空；active 必须为非空 generation。PID、时间戳或 TTL 均不参与抢占决策。首次发布 key 目录时包含合法 unseeded state；只有 clone 完整发布才切 ready；任何 gateway/projection/provider 使用前先同步落盘 active。

`seed.json` 记录 `version:1`、`driver_type:string`、`source_dir:string`、`entries:[]`、`fingerprint:string`。每个 entry 固定为 `path:string`、`kind:"file"|"directory"|"auth-link"`、`mode:uint32`、`digest:string`；path 是相对 profile 的安全路径，按字节序排序、无重复、无绝对路径或 `..`。普通文件保存内容摘要，目录保存存在性，auth-link 的 digest 为空。列表只覆盖首次实际种入的 provider-visible 资源；不收 session 内容、token 或认证内容。fingerprint 覆盖 canonical entry 列表。上限沿用物化指纹限制：20,000 entries、64 MiB 读取字节。seed 一旦发布不从 source 重算，不含可变进程数据。

### 3.3 权限与非 symlink 边界

- 新建 namespace/key/profile/staging 为 0700，marker 和 lock 为 0600。已有 SDK 目录 Unix 必须属于 euid 且 group/other 无权限；已有 marker/lock 必须是同 owner 的单链接普通文件、0600。不能先 Chmod 收编不安全目录。
- Windows 必须设置并读取回受保护 DACL：仅当前用户 SID 与 LocalSystem 允许全部访问，关闭继承；已有 root/key/marker/lock 也验证该 ACL，不能把 `os.Chmod(0700)` 当 Windows 权限验证。不能证明权限的卷返回 `ErrUnsupportedFilesystem`。
- 对从已验证 namespace 句柄到 key、marker、lock、profile 的每个组件做不跟随检查；拒绝 symlink、Windows reparse point/junction、marker hardlink、锁句柄与路径身份不一致。通过 `os.Root`/句柄相对操作限制写入边界；不要先检查字符串路径后用另一条未经验证的路径写入。
- source/auth 是既有 `AuthLink` 的特定例外：仅 provider 已声明的 auth 文件可链接到 canonical source 对应 auth 文件；重开时核对链接身份，禁止把 auth 例外扩成目录/配置/manifest 例外。SDK 不读取认证内容入 fingerprint；provider OAuth 通过共享 auth 文件自行更新是原有 AuthLink 语义。
- SDK 写入所有控制文件前后验证对象身份；OS 锁保护遵守本合同的进程，私有权限隔离其他 OS 用户，不声称隔离恶意同账号或管理员在持锁期间任意替换整棵树。

新 namespace/key 用相同父目录下独占创建的私有 staging，写齐 marker 并 Sync 后以不可覆盖已有非空目录的 rename 发布；竞争 loser 只删除自己本次持有、已核对 generation 的 staging，然后验证 winner 目录。已有无 marker 目录稳定拒绝，不“补标记”，不删除。每个 `.seed-<generation>` 在 seed callback 前写入 0600 的 `.agent-adaptor-seed-owner.json`，其封闭字段为 `version:1`、`key_hash:string`、`generation:string`；仅匹配 key/目录 generation 的 staging 可在持锁下清理。该 marker 在初始化成功后删除，不进入 provider 资源指纹。初始化中断后仅 unseeded 状态且 marker 匹配的 staging 可清理；已存在 profile 或 unknown staging 不能当空目录重种。

## 4. 专用锁与依赖决定

使用已经存在的 `golang.org/x/sys v0.41.0`，T04 将它从 indirect 移到 direct，版本不变，不新增 flock 库。实现仅位于 `internal/hostedprofile`：

| 平台 | 已验证句柄与独占操作 | 释放与争用 |
|---|---|---|
| Linux/macOS | 0600 的 O_RDWR、O_CLOEXEC、O_NOFOLLOW 句柄；fstat 核对 owner/type/nlink 与路径；`unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)` | EWOULDBLOCK/EAGAIN → ErrInUse；`LOCK_UN` 后关闭句柄；进程退出 OS 释放 |
| Windows | `CreateFile`、`OPEN_EXISTING`、`FILE_FLAG_OPEN_REPARSE_POINT`，不继承句柄，share read/write 而不 share delete；检查 reparse、file ID、DACL | `LockFileEx` 以 EXCLUSIVE + FAIL_IMMEDIATELY 锁 offset 0 的 1 byte；LOCK_VIOLATION/IO_PENDING → ErrInUse；UnlockFileEx 后 CloseHandle |

只用 exclusive/nonblocking，不用共享锁、锁升级或轮询 TTL。每个 Agent、每个 key 各有自己的句柄；同 Agent 同 key 通过 `toolProfileMu` 复用一个 Claim，不重复锁；不能将全进程缓存的“已锁”误当另一个 Agent 也拥有锁。等待 Agent 本地 claim 初始化必须支持 ctx 取消（可用带 context 的单令牌门），不能让慢 GetProfile 或文件 IO 把其他取消的运行永久堵在 mutex。

R007：Windows owner.lock 使用专用长期 `CreateFile` 句柄并禁止 `FILE_SHARE_DELETE`；临时 ACL 检查句柄或 `os.Root.OpenFile` 的默认分享模式不能替代。保留 DACL、reparse、hardlink 和文件身份校验，原生 Windows fixture 必须证明持锁期间 rename/delete 被 OS 拒绝。

执行前后检查 `ctx.Err()`；争用一次返回 ErrInUse，没有后台无限等待。Close 持有 claim 的释放阶段不等待别人释放锁。按 R007 区分 readyWritten、unlocked 和剩余句柄清理阶段：成功 OS unlock 是所有权释放点。解锁前错误保留锁；解锁后错误仅重试原句柄清理，永不重写 state、重新加锁或触碰接任者。pending cleanup 仍保留在 Agent map，但不再宣称持锁。OS 资源调用的有界性限定在支持的本地磁盘：Linux ext4/XFS/tmpfs、macOS APFS/HFS+、Windows 本地 NTFS/ReFS。拒绝已识别 NFS/SMB/FUSE/远程卷和未知不具备可靠锁/ACL能力的文件系统；不自动退到内存锁或 lease 文件。跨进程探针属于测试，不在每次 SDK 运行偷偷启动探针进程。

依赖选型三项评价：

1. 可靠性：x/sys 提供 OS 的正式锁、文件身份和 Windows ACL 绑定。代码只是持有已验证句柄的薄封装；不手写基于 PID、年龄或删锁的分布式锁算法。已有 profilestate 锁不适用于 Agent 生命周期。
2. 维护：x/sys 是 Go 官方维护的现有依赖，有公开源码、版本和 Go issue/security 流程。精确保持 v0.41.0；无需为这个变更升级其他模块。
3. 局部化：新增 direct import 仅在私有 hostedprofile 平台文件，公开 profile 只发行错误词汇。根包不暴露锁类型、数据库或服务依赖。

比较对象是 [gofrs/flock v0.12.1](https://raw.githubusercontent.com/gofrs/flock/v0.12.1/flock.go)：有 TryLock 和多平台实现，但它从路径内部开文件、不接受调用方已验证的句柄；采用后仍要另加句柄安全层。这里新增依赖的收益不足，故不采用。平台语义依据 [Unix 实现](https://raw.githubusercontent.com/gofrs/flock/v0.12.1/flock_unix.go) 与 [Microsoft LockFileEx](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-lockfileex) 对照核验；未把该文档核验计作原生 Windows 实跑。

## 5. 私有操作合同及正常状态机

T04 在 `internal/hostedprofile/claim.go` 实现下列签名；它们不是公共 SDK/SPI，不可被 root 类型 alias/字段/签名泄露：

```go
type Spec struct {
    DriverType string
    SourceDir string
    Identity driver.AgentIdentity
}

type Claim struct { /* 私有字段持有已验证目录、锁句柄和 generation */ }

func Acquire(ctx context.Context, spec Spec) (*Claim, error)
func (c *Claim) Dir() string
func (c *Claim) Initialize(ctx context.Context, seed func(context.Context, string) error) error
func (c *Claim) BeginUse(ctx context.Context) error
func (c *Claim) ReleaseUnused(ctx context.Context) error
func (c *Claim) ReleaseClean(ctx context.Context) error
```

`Initialize` 回调的 string 是私有 staging 目标，最多调用一次；返回前必须完成种入资源验证及 seed.json。ready 重开完全不调用 seed callback，不再次 `GetProfile(Clone)`；返回的执行 selection 固定为 `Dedicated(claim.Dir())`。`BeginUse` 写并 Sync active generation 后才成功；同 Claim 重复调用幂等，不给新 generation。`ReleaseUnused` 仅允许尚未 BeginUse 的 claim，不能将 active 变 ready。`ReleaseClean` 的唯一生产调用者是 Agent.Close 最后释放阶段，其前置条件是全部 admitted run drained、全部 provider writer 确认退出、owned MCP 投影清理完、gateway 已确认关闭；顺序不能反转。nil/无效 Claim 方法必须稳定错误或 documented nil-safe release，不阻塞。

| 当前状态 | 操作/证据 | 下一状态与效果 |
|---|---|---|
| 未拥有 | 目录/marker 验证、TryLock 成功、state 非 active | held-unseeded 或 held-ready；仍无 gateway/provider |
| 未拥有 | 锁争用 | ErrInUse；不读取其他 owner 的运行态，不改源/clone/store |
| held-unseeded | Initialize 完整成功 | 原子发布 profile + seed，再写 ready；锁继续持有 |
| held-unseeded | seed 失败/取消 | 只清本次 staging；ReleaseUnused；不发布半 clone |
| held-ready | BeginUse 同步成功 | held-active；可以使用执行 profile / 交付 runtime |
| held-active | Run 完成、失败或单轮 Cancel | 继续持有；不得 per-run 解锁或删除 provider 文件 |
| held-active | Close 在 OS unlock 成功前失败/超时 | closing-retry，拒绝所有新 Agent/Thread 运行，保留锁与 generation |
| unlocked | 原 lock/root 句柄 Close 失败 | pending cleanup，允许接任者持锁；旧 Agent 仍拒绝新运行，重试只关原句柄，不改 state 或接任者锁 |
| held-active | Close 的所有前置条件满足 | ReleaseClean：写并 Sync ready → 解锁 → 关句柄 → 删除 Agent 内存 claim |
| held-ready | 未启动任何运行的 Close | ReleaseUnused；可安全接力 |

正常首次/重建的唯一操作顺序：

1. 在既有准入内校验工具/MCP config；只读解析 Dedicated source；确定 key 并获取 claim。Acquire 成功立即注册到 Agent 的 claim map，早于 Initialize/BeginUse 等可能失败的写操作；后续失败仍由 Close 找到并释放，不能丢失已锁句柄。
2. 验证路径/manifest/权限；拒绝 active 旧 generation。首次调用 Initialize，重开只验证既有 seed 与 profile，禁止覆写 transcript。
3. 先检查并移除可证明 SDK-owned 的旧 hosted MCP 投影。原资源 manifest 的 owner、provider 路径与 rendered fingerprint 必须全部一致；不明/冲突 key 返回既有 InvalidMCPConfig，篡改控制载体返回 ErrUnsafe。去投影失败不启动新 endpoint。
4. 已注册 claim 的 BeginUse durable active 后，通过现有 runtime attachment 生成本 Agent 的 endpoint/token/env carrier，再由唯一 resolved request 传给 Driver。BeginUse 写入/Sync 不确定时保留 claim 和不确定 active 状态，Close 在证明没有 provider/gateway 使用或已完成全部退出后重试安全清理；不把该错误路径冒充未写过状态而直接放弃句柄。
5. claim/兼容性读取必须与物化相互协调；完整 Thread compatibility 计算完毕后才允许 Driver。provider parser/checkpoint 语义不变。正常重建能获取 ready claim，证明前 owner 的 writer、投影、gateway 和 ready 持久化步骤完成；其 unlock 后的句柄清理仍可能失败。旧 active 绝不冒充上述证明。

Close 的确定顺序：先关闭准入并 cancel → 保留现有第一次 CloseProcesses 以解除 Run 阻塞 → drain → 第二次幂等 CloseProcesses 回收晚启动 writer → 移除 owned MCP 投影 → 关闭 gateway → 删除 owned 临时 clone / 为持久 claim ReleaseClean → complete。第一次进程回收是 drain 的辅助手段，不是绕过 drain 的权限。任何非 nil 的进程回收、去投影、gateway Close、写 ready 或释放错误都返回 `complete=false`，下一次 Close 使用新的 context 重试；包括非 context 的 gateway 错误。

去投影成功也暂不丢 claim。多 identity 场景逐项记已完成阶段，失败后不重复删除不属于本 Agent 的内容。若用户改过 hosted MCP 项，遵循既有 rendered fingerprint 保护，只移除失效的所有权记录、保留被改内容；这不等于删除了外部项。随后关 gateway 撤销本 Agent token。不能用持久目录保留作为跳过去投影的理由。其他无法完成安全去投影的错误仍保留 gateway/claim 供 Close 重试，不能把未完成的 Close 声称为凭据已撤销。

## 6. 崩溃和离线恢复

所有权和 writer 健康是两个证明：内核锁只证明此刻没有另一个合规 Agent 持锁；ready state 才证明上一使用 generation 完成了正常退出序列。

- 在 Acquire/Initialize 后、BeginUse 前退出：OS 锁最终释放，下一 Agent 可获取 ready；unseeded 的 owned staging 按 §3 清理后再初始化。fixture 必须确认没有 provider spawn。
- 在 BeginUse 后退出，包括 prompt 之前但 active 已落盘：下一 Agent 获取锁后看到 active，释放自己刚取得的锁并返回 ErrRecoveryRequired。它不接管、不杀未知 PID、不修改 MCP 或 state；无按时间自动“变健康”。
- 如果锁仍占用，优先 ErrInUse；即使 ctx 有很长 deadline 也不等到 TTL 后偷锁。

离线恢复步骤冻结为宿主运维流程，不新增 root repair API：停止使用此 namespace 的全部宿主进程；按宿主进程管理记录确认并回收该 generation 的 provider 进程树，等待退出；备份整个 key 目录（包含 transcript）；用同一 OS 独占锁和 marker/权限校验工具链进行维修，按现有 MCP owner/rendered fingerprint 规则仅移除 owned hosted entry，验证不剩可用旧 gateway；最后原子将 active state 改为 ready，再解锁。无法证明 writer 全部退出、投影归属或目录安全时保留备份与 active 状态，不编辑 marker 强行运行。运维也可在所有进程停用后把完整 namespace 移到隔离备份，再由 SDK 新建；这会失去本地文件续接，必须按既有 resume reject 合同处理，不能假称恢复了旧会话。

永久清理由宿主在该 namespace 停用并完成上述退出/备份/独占确认后执行。Agent.Close 不是清除历史会话的 API。手工删除时不能只删 owner.lock；删除整个 namespace 前必须排除任何并发 opener。

## 7. 指纹与物化保真

目录 key 只决定文件归属。Thread compatibility 必须继续包含构造期 Driver config、模型、完整 identity、解析后的 workspace、Tools Revision/schema、skills、MCP、instructions、profile resources、runtime semantic fingerprint 和未来已冻结的 append prompt 等维度。process signature 保留实际 profile 路径、实际 URL、实际 env carrier 与物化 payload；不把 process signature 换成 namespace hash。

按 R003，T04 使用既有 claim 钩子完成所有权与冷会话文件保护；最终已解析资源快照及 W02-R06 由下一批 T06 接入唯一 invocation 管线，不依赖同批 T05 的新代码。物化指纹必须从实际文件观察，seed 只证明初始来源和资源边界，不能以 seed hash 永久代替实际内容。seed 后 source 的 settings/MCP/skills 改动不会隐式再复制进已有 clone；新的声明通过现有 desired resource 机制进入执行环境。source 被替换成不同目录身份则拒绝采用原 namespace。

规范化规则是封闭白名单，不递归搜索 JSON 猜 endpoint：

1. 读取明确配置根（下表）与 seed/manifest/本轮声明中指向的额外实际资源路径，严格 canonical JSON/TOML（含数字类型）或文件字节摘要、文件 mode、相对路径排序。认证内容、session/transcript、lock/generation/时间戳不进入兼容性指纹。配置对象的未知字段及未知 manifest kind 所指向的安全资源仍纳入实物摘要，不能按 `.agent-*` 等宽泛前缀忽略或递归把 session 目录当配置扫描。
2. hosted MCP 只在 key = `toolidentity.ServerKey`、owner = `toolidentity.ManifestOwner`、provider layout/path、rendered fingerprint 均吻合时，从**实物基线视图**移除该 entry；空 MCP section 移除，纯空配置对象与不存在视为同一空基线，避免首次尚未投影与次轮已有投影产生差异。真实 req.MCP 不修改，其**兼容 payload**继续使用现有 Tools semantic URL/env token。token value 不进入摘要。丢失/篡改 manifest 或与 rendered value 不符时不做规范化，按 collision/unsafe 拒绝。
3. 普通 SDK-owned MCP 只有实际 value 与 manifest 及本轮 resolved MCP 渲染值一致时才作为本轮 desired entry。在纯计算视图按与 SyncResource 相同的 overlay/prune 规则投影本轮明确声明的普通 MCP，使首次缺 entry 与下次已存在相同 entry 的最终语义相同；外部非目标 key 全量保留。对于本轮会 prune 的旧 managed key，必须有路径与原渲染摘要证明后才能移除。缺少可验证原渲染摘要时保守保留实际内容，不把 `managed=true` 本身当证据。T04 在 `internal/mcpruntime/resource.go` 为新写入的所有 MCP entry 记录 `rendered_fingerprint`，保留 hosted collision 的更严格 owner 要求。
4. skills 以 manifest 的精确 runtime path、source identity/source_hash 与当前目标内容核对后，按已解析 `ResolvedSkills` 的实际目标投影。未声明/外部 skill 的真实文件继续计入。越界 symlink 不被泛化为“managed”；仅已校验的 skill target 允许读取其被声明的 source，并记录实际内容 hash。T06 在 internal/skillruntime 内复用同一资源规则，不能只靠 skill 名或 source path 字符串。
5. settings、config patch、instructions、hooks、profile agent 等没有足够 rendered ownership 证明的内容，始终读取实际文件并参与摘要；绝不为了冷续接而全部排除。它们的 desired payload 仍独立进入完整 fingerprint。首次 A 执行才产生这些未证实归属的物化痕迹时，B 可以保守不兼容；`ResumeOnly` 拒绝，continue-or-start 按原安全规则新建。该保守行为必须在文档及 fixture 中显式呈现，不能把未知痕迹描述成“同状态已证明可续接”。
6. 同一已物化语义状态、完整相同声明且仅 hosted endpoint/token 轮换时，Run 与 Stream、同 Agent 和正常 Close 后的新 Agent 应有相同 Thread compatibility；规范化结果不得因 Go map 顺序、源路径等价写法或新 gateway 随机数不同而漂移。对任何未被规范化的实际资源变化，指纹必须改变或启动前拒绝。

| Driver | 固定配置/资源根 | 不哈希内容的 auth 例外 |
|---|---|---|
| codex | config.json、config.toml、instructions.md、skills | auth.json |
| claude | settings.json、config.json、.claude.json、skills | .credentials.json、credentials.json |
| cursor | config.json、settings.json、mcp.json、skills | cli-config.json、auth.json、credentials.json |
| codebuddy | settings.json、.mcp.json、mcp.json、skills | .credentials.json、credentials.json |

每次 claim（包括同 Agent 已缓存 selection）先完成当前目录/所有权的只读检查。按 R003，最终不可变 snapshot 必须在唯一 skills 解析及注入之后、Thread fingerprint / Driver 派发之前从实际 resolved 资源生成，归属 T06。不得以提前 claim 的共享 selection cache 代替本轮快照；允许 stabilizer 返回 IO/安全错误，不可塞入字符串或吞掉。不得增加第二套 skill 解析或执行管线；无状态运行也保留必要的目录/资源安全验证。

只在 pure compatibility view 上做投影，不把该视图写回 provider profile，也不替代 Driver 的正式物化。辅助函数不得生成 provider Transcript/checkpoint。T04 冷重建主 fixture 使用真实 session 文件及可证明的 MCP 物化；T06 补动态 skills 的首次物化与后续链接内容变化 fixture；额外 fixture 固定未知 config 物化保守拒绝的边界，不用其 skip 掩盖主保证。

## 8. 文件分配与 fixtures

本合同的早期 claim、所有权与 Close 实施由 T04 承担；R003 的最终已解析快照由 T06 在其授权路径交付。下表记录 T04 原始分工，后续精确所有权以当前 manifest/task.json 为准：

| 精确文件/目录 | 责任 |
|---|---|
| `profile/errors.go`、`profile/selection.go`、`profile/doc.go`、`profile/*_test.go` | 四 sentinel、Dedicated/临时/崩溃恢复 godoc、errors.Is 合同 |
| `internal/hostedprofile/{claim,manifest,path,seed}.go` | 编码、marker、claim 状态机、安全初始化；新增所有类型为私有边界 |
| `internal/hostedprofile/platform_unix.go`、`platform_windows.go`、`platform_unsupported.go` | OS 句柄锁、权限和本地文件系统能力判定；unsupported 必须结构化失败 |
| `internal/hostedprofile/*_test.go` | key golden、恶意目录、进程竞争/崩溃、锁持有与重试；测试子进程使用 Go 测试二进制，无 shell/CLI 依赖 |
| `tools.go`、`tools_contract_test.go` | 持久/临时分流、仅首次 clone、兼容视图、claim 注册与 MCP 清理 |
| `agent.go`、`agent_close_test.go` | 两阶段 profile 清理、gateway 后解锁、全部错误 retryable |
| `internal/mcpruntime/**`、`internal/skillruntime/**` | 精确 owned entry 验证/指纹投影和安全 clone；不修改 provider 协议解析 |
| `alignment_profile_test.go` | 下列 root integration fixture，名字均以 TestAlignmentProfile 开头 |
| `go.mod`、`go.sum` | x/sys v0.41.0 direct；不升级/增加其他运行时依赖 |
| `handoffs/T04/documentation.md` | G01 合并文档，T04 不直接编辑集中 docs/CHANGELOG |

不新增 root/SPI 导出声明，root/SPI AST golden **不变**。profile 的新导出错误使用公共合同测试审阅。C01 仅写此合同和自己的 documentation，未实现这些 Go 文件。

| Fixture 名称后缀（TestAlignmentProfile…） | 必须能区分错误实现的断言 |
|---|---|
| DedicatedColdResume | fixture provider 首轮正式成功且在 `profile/projects/<provider-session-id>.jsonl` 写含不可猜 nonce 的记录；次轮只有读取该记录并核对 nonce 才成功。A.Close 后 B 同 identity/key 必须同目录、续接旧 checkpoint；旧临时删除实现必失败。分别经 Run 和 Stream 验证 |
| CredentialsRotate | A/B URL、env carrier、token hash 均不同；A endpoint 不能再调用，磁盘无 A 可用 hosted entry；source 哈希前后相同（auth provider 更新单独断言），Raw/metadata 没有 bearer |
| SameProcessConflict / CrossProcessConflict | A 持有时 B 在启动前 errors.Is(ErrInUse)，Driver 调用数为 0；无等待 TTL；A.Close 成功后 B 成功；child 用 stdin/stdout 屏障证明持有区间 |
| IdentityIsolation / KeyEncoding | 逐个改变 ID/TenantID/ProfileID/Name 得不同 key；`["ab","c"]` 与 `["a","bc"]`、NUL、分隔符、中文、空值等不混淆；等价 source alias 归一；不在路径泄露 Thread key |
| UnsafeOwnership | missing/未知版本/重复 JSON key/身份篡改 marker、world-readable 控制目录、source inode 替换、marker hardlink、profile/lock symlink、Windows junction/DACL 逐一拒绝；外部目标字节不变 |
| SeedOnlyOnce | seed callback 计数一次；重开时 session 与 provider 写入的非种子文件保留；source settings 后改不重种 clone；不调用会创建 source 的 GetProfile |
| CrashBeforeUse / CrashDuringUse | 子进程直接退出后锁可再获取；ready 可接力；active 得 ErrRecoveryRequired 且 state/session 未改。运行中 crash 可保留独立 child writer 验证绝不启动 replacement |
| CloseRetry | inject process-close、drain deadline、MCP cleanup、gateway-close、state Sync、unlock 各阶段失败；准入保持关闭，解锁前 B 被拒绝。另注入成功 unlock 后 lock/root close 错误，允许 B 接管，A 重试不得改写 B 的 active marker 或解开 B 的锁；旧 writer 退出先于接任 projection/spawn，持久 session 保留 |
| StaleCarrier / MCPConflict | 合法旧 owned 投影只精确移除；key 被外部占用、env carrier 被另一个 server 使用、篡改 rendered fingerprint 均 fail-closed；不误删 external server |
| NativeCleanup / CloneCleanup | Native/Default/CloneFrom/CloneNative 仍临时；Close 只删合法 temp 直接子目录，source 保留；Dedicated 不走临时 RemoveAll |
| Compatibility / MaterializedDrift | 只换 endpoint/token 兼容；变 model/Tools Revision/skills/MCP/instructions/config/完整 identity/workspace/runtime semantic fingerprint 必须不兼容；managed MCP/skill 与外部文件篡改分别验证；未知 config 首次物化痕迹按 §7.5 保守拒绝 |
| MissingHistoricalSession | 有 healthy store checkpoint 但 session 文件不存在，fixture provider 正式拒绝 resume；continue-or-start 最多一次新会话，ResumeOnly 不新建；旧健康记录只在新健康 checkpoint Finalize 后替换 |

fixture provider 只在测试 Driver 中识别明确测试协议，不让 process helper 猜 session。root fixture 不编译或调用真实 provider CLI。T04 必跑任务中的两条完整命令；T20 独立复核断言，T26 原生 Windows 跑锁/ACL/child 进程场景，T27–T30 在显式 live 双门和隔离 profile 下验证各正式 provider 会话文件。macOS 单测、交叉编译、格式校验均不能代替这些平台/live 证据。

## 9. 验收及迁移裁决

C01-AC01 由 §§1、5、6 与 NativeCleanup/MissingHistoricalSession fixture 覆盖；C01-AC02 由 §§3、4、6 与双 Agent/双进程/identity/Windows fixture 覆盖；C01-AC03 由 §§5、7、8 的具体签名、状态机、依赖、文件 owner 和失败行为覆盖。W02-R01–R08 的实施/独立验证仍由任务图原 owner 承担，C01 不把其状态标为完成。

G00 必须在本批中央文档重新打开 AGENTS §14 的 Thread/profile 兼容子项，记录：专用持久目录、跨进程所有权、dirty generation 拒绝和完整物化指纹需后续证明。G01 在 T04 实现验收时同步公开语义和 CHANGELOG；只有后续独立平台/live 证据闭环才关闭 W02。internal 的裸 sibling、SDK/SessionKey API、只凭 resume ID 的测试以及扩大任意目录删除范围均不采用。
