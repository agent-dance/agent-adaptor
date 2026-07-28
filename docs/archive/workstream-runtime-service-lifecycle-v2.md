# Workstream: Runtime Service Lifecycle V2

## 1. 目标

把 runtime service 从“ensure 出来的 endpoint 列表”升级成更稳定的运行时资源合同。

## 2. 价值

- 对 `paperclip`：更容易管理共享 dev server / db / cache
- 对 operator：更容易知道 service 是否健康、是否可复用、什么时候该停
- 对 adapter：可以拿到更真实的 runtime context，而不是只看 URL

## 3. 用户场景

- 多次运行复用同一个本地 dev server
- 某个运行只需要临时 service，结束后必须释放
- 宿主想区分 shared / ephemeral 资源策略

## 4. 规划范围

要补的能力：

- lifecycle: `shared` / `ephemeral`
- `reuseKey`
- health / status 扩展
- richer `RuntimeServiceReport`
- 更清楚的 cleanup / stop policy

## 5. 当前已落地

当前 core 已经支持：

- `RuntimeServiceSpec` 显式声明 `Lifecycle`、`ReuseKey`、`Command`、`CWD`、`Port`
- `RuntimeServiceRef` 和 `RuntimeServiceReport` 保留这些字段，并携带 `Status` / `Health` / `OwnerAgentID`
- `prepareRuntime()` 会在 manager 返回的 ref 上补齐 spec 中未丢失的生命周期信息
- `RunResult.RuntimeServices` 会保留这组 richer runtime metadata
- runtime prompt/context 注入会显示 lifecycle 和健康状态

这意味着宿主现在已经可以把 runtime service 当成“带生命周期与复用语义的资源描述”，而不是单纯 URL 列表。

## 6. 边界

仍然不进入 core：

- docker orchestrator
- tmux supervisor
- process manager
- 具体的 service hosting 实现

core 只定义合同，实际拉起/复用逻辑仍由 `RuntimeServiceManager` 实现。

## 7. 验收标准

- 宿主可以明确声明 runtime lifecycle
- `RunResult.RuntimeServices` 能稳定表达共享与临时资源状态
- release 规则对宿主和 operator 都可解释

## 8. 推荐实施顺序

1. 扩展 type contract
2. 更新默认 runtime manager helper
3. 为 runtime report 加测试
4. 最后补文档与 example
