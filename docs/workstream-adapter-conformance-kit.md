# Workstream: Adapter Conformance Test Kit

## 1. 目标

把 `DriverAdapter` 的最低兼容合同沉淀成可复用测试套件，避免宿主和 adapter 作者每次都靠人工经验验收。

## 2. 价值

- 对 `paperclip`：接新 adapter 时更快判断“是否真的兼容”
- 对 SDK 本身：contract 变更时能第一时间暴露 breakage
- 对第三方 adapter 作者：知道要实现到什么程度才算合格

## 3. 用户场景

- 新接入 `gemini` / `opencode` / `pi` 之类 adapter
- 升级 SDK 后回归验证旧 adapter
- 在 CI 中把 adapter contract 作为稳定验收门槛

## 4. 范围

当前阶段纳入 test kit 的内容：

- descriptor 基本形状
- config 校验
- environment / model / config schema / quota 控制面输出形状
- `SessionCodec` round-trip 与 guard fingerprint 稳定性
- `ListSkills` / `SyncSkills` 快照 truthfulness

当前阶段不放进 test kit 的内容：

- provider live auth / quota 网络探测
- 真正执行 CLI 的协议细节
- UI 或 server 层渲染契约

这些由 adapter-specific execution tests 继续覆盖。

## 5. 当前落地

本仓库已经提供：

- 公共包 `adaptertest`
- `adaptertest.Run(t, adaptertest.Subject{...})`
- built-in `codex` / `claude` / `cursor` conformance tests

## 6. 设计原则

- 不假设 live provider account
- 不把 provider-specific 细节硬塞进公共 kit
- 只验证 adapter SPI 合同中真正稳定、宿主可依赖的部分

## 7. 使用方式

```go
func TestMyAdapterConformance(t *testing.T) {
	adaptertest.Run(t, adaptertest.Subject{
		Name:    "my-adapter",
		Adapter: myadapter.NewAdapter(),
		Config:  myadapter.Config{Command: "my-agent", Model: "stable"},
		SessionState: &agentadaptor.DriverSessionState{
			ResumeID: "session-1",
			Data: map[string]string{
				agentadaptor.SessionParamCWD:         "C:/repo",
				agentadaptor.SessionParamWorkspaceID: "workspace-a",
			},
		},
		RequiredSessionKeys: []string{
			agentadaptor.SessionParamCWD,
			agentadaptor.SessionParamWorkspaceID,
		},
		RequiredConfigFields: []string{"command", "cwd", "model"},
	})
}
```

## 8. 验收标准

- 新 adapter 引入一条 conformance test 即可获得基础合同验证
- built-in adapters 全部通过该套件
- contract 变更时能稳定触发 test failure，而不是静默漂移

## 9. 后续扩展

后续如果要继续加深，应新增而不是重写当前 kit：

- richer transcript contract checks
- runtime lifecycle v2 contract checks
- 可选 execution harness hooks
