# Workstream: Bridges, Profiles, and Host Layer Examples

## 1. 目标

降低宿主接入成本，但不污染 core 边界。

## 2. 价值

- `paperclip` 这类宿主可以少写 glue code
- 直接使用 SDK 的团队更快得到可运行参考
- 保持 core 纯净，同时提供更高层复用件

## 3. 内容

### 3.1 Process Adapter Bridge

把本地进程协议桥接成 `DriverAdapter`。

### 3.2 HTTP Adapter Bridge

把远程 sidecar / service 形态桥接成 `DriverAdapter`。

### 3.3 Profile / Resolver Package

把业务角色解析为 `AgentBinding`，但不替 core 做自动路由。

### 3.4 Minimal Service Host Example

演示如何把 core 嵌进最小服务壳中。

## 4. 用户场景

- 宿主已有业务 profile，如 `default-coding`、`review`
- 宿主已有 HTTP 或 sidecar adapter，不想重写执行合同
- 团队想先接一个最小服务壳，而不是直接上完整 `paperclip`

## 5. 边界

这些都应该做在 core 之上，而不是回灌进 core：

- HTTP/gRPC server
- queue / scheduler / planner / router
- tenant DB / company skills service
- plugin store

## 6. 验收标准

- 新包或 example 能直接复用 core `Run/Start/Admin` 语义
- 不引入第二套执行入口
- 不让宿主重复实现 session / skills / runtime glue
