# Workstream: Session Codec

## 1. 目标

把 adapter-specific session 参数从“隐含在 `DriverSessionState.Data` 里的约定”升级成正式合同。

## 2. 价值

- 宿主不再需要猜 map key
- adapter 作者明确哪些字段决定 resume guard
- session 持久化与调试可以稳定读取结构化参数

## 3. 用户场景

- `paperclip` 把 session 状态持久化到自己的存储层
- operator 需要排查为什么 resume 被拒绝
- adapter 作者需要声明 cwd / workspace / prompt bundle 等 guard 语义

## 4. 当前合同

当前 core 暴露：

- `SessionParams`
- `SessionCodec`
- `SessionCodecAwareDriver`
- `SessionCodecFor(driver)`

内置 adapter 的稳定 guard key：

- `codex`: `cwd`, `workspace_id`
- `claude`: `cwd`, `workspace_id`, `prompt_bundle_key`
- `cursor`: `cwd`, `workspace_id`

## 5. 当前落地

### 5.1 core

- `persistSession()` 与 resume path 都走 codec 归一化
- passthrough codec 仍然存在，保证非显式 codec driver 也能 round-trip

### 5.2 built-in adapters

- 全部显式实现 `SessionCodecAwareDriver`
- `Run()` 中的 resume guard 与 codec 使用同一组 key
- `checkpoint.State.Data` 使用统一常量写入

### 5.3 测试

- core fallback codec round-trip 测试
- built-in conformance tests
- built-in mock CLI execution tests，验证 checkpoint / resume / reject

## 6. 使用方式

```go
codec := agentadaptor.SessionCodecFor(claude.NewAdapter())
params := codec.ToParams(record.DriverState)

fmt.Println(params.DisplayID)
fmt.Println(params.Values[agentadaptor.SessionParamPromptBundleKey])
fmt.Println(codec.GuardFingerprint(params))
```

不要再直接把 `DriverSessionState.Data["some_key"]` 写死在宿主里。

## 7. 验收标准

- resume-capable built-in adapter 全部显式实现 codec
- codec 与 `Run()` guard 行为一致，不允许“一套写入、一套校验”
- host 能通过 codec 读取稳定 session 参数

## 8. 非目标

- 不改变 `SessionStore` 责任边界
- 不引入第二套 session 执行入口
- 不承诺跨 adapter 统一一套业务语义，只承诺读取方式统一
