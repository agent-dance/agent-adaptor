// Package tests holds host→SDK 黑盒验证套件，用于验证 codebuddy driver 的功能性
// 是否符合预期。所有用例都从 host 侧 agentadaptor.New + SDK.Start 发起，观测
// SDK 对外暴露的结果/事件/快照，不直接调用 driver 内部私有函数（那属于既有单元
// 测试，本套件不与之混淆）。
//
// 本包结构：
//   - helpers_test.go        共享 helper（CLI 探测/隔离配置/host SDK 装配/事件录制）
//   - *_live_test.go         真实 CLI 端到端用例（build tag codebuddy_live）
//   - mcpserver/             受控 MCP server，用于证明真实 CLI 读取并调用 host 注入的 MCP
//   - probe/                 抓帧探针（可执行工具，非测试；拉起真实 CLI dump 原始帧）
//   - probe/fixtures/        探针落盘的真实帧，作为全部验证预期的事实来源
package tests
