# Workstream: Normalized Transcript Contract

## 1. 目标

在现有 `RunEvent` 基础上补一层稳定、可选的 transcript 语义，让宿主能统一消费不同 adapter 的执行输出。

## 2. 价值

- `paperclip` 或桌面端 UI 更容易统一渲染
- operator 更容易区分 assistant、tool、summary、failure
- 减少 provider-specific JSON parser 在宿主侧的重复实现

## 3. 用户场景

- 宿主需要展示统一 transcript
- 宿主需要把 tool call 和 assistant 文本分开处理
- 宿主需要结构化 summary / failure 信号

## 4. 规划范围

- transcript item types
- 与现有 `RunEvent` 的映射
- 最低 built-in adapter 实现
- 宿主消费建议

## 5. 当前已落地

当前 core 已经提供：

- `TranscriptItem`
- `RunResult.Transcript`
- `DriverRunResult.Transcript`
- `RunEvent.Data["transcript"]`，用于 CLI `stdout` / `stderr` 实时事件

当前 built-in adapter 的保守 item types：

- `output`
- `diagnostic`
- `structured`
- `summary`
- `question`
- `failure`

其中 `structured` 代表“这一行是可解析 JSON”，不是“SDK 已经理解 provider 私有协议全部语义”。

## 6. 关键约束

- 必须向后兼容现有 event stream
- 不创建第二条执行入口
- 不为了统一而过度抹平 provider-specific 细节

## 7. 验收标准

- 宿主不依赖 provider-specific 原始 JSON 也能拿到核心 transcript 语义
- built-in adapter 至少能给出可用的基础 transcript item
