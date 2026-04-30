# Task recipes · cookbook

[English Version](./recipes-cookbook.md)

6 条常见"剧本范式"，每条 5–8 行核心字段定义。把你家固化任务对照最相近的一条，复制 → 改名 → 改字段，10 分钟内就能上线一条新剧本。

> 所有范式都假设你已经有一份 `Recipes(cfg, instructionsDir) map[string]Recipe` 字典（参考 `recipes.go`）。每个 recipe 函数返回 `Recipe{ Name, Description, Trigger, Resources, Prompt }` 五字段。

---

## 1. `incident-hotfix` — 值班响应任务

适用：on-call 收到告警 → 自动起 agent 跑诊断 / 提 hotfix → 严格审批 → 写复盘。

```go
return Recipe{
    Name: "incident-hotfix", Description: "On-call response, blast-radius first.",
    Trigger: "per-run via WithProfileResources",
    Resources: agentadaptor.ProfileResources{
        Skills: []agentadaptor.SkillRef{agentadaptor.Key("incident-diagnostics"), agentadaptor.Key("rollback-runner")},
        Agents: []agentadaptor.AgentSpec{{Key: "incident-reviewer", RuntimeName: "incident-reviewer", Description: "Incident hotfix reviewer"}},
        Hooks:  []agentadaptor.HookSpec{{Key: "post-run-summary", Event: agentadaptor.HookEventStop, Handler: agentadaptor.HookHandler{Type: agentadaptor.HookHandlerCommand, Command: "incident-summary"}}},
        Instructions: &agentadaptor.InstructionsBundleRef{ID: "incident-hotfix", Path: filepath.Join(dir, "incident-hotfix.md"), Scope: agentadaptor.InstructionScopeRun},
        Config: []agentadaptor.ProfileConfigPatch{{Key: "approval", Capability: "approval", Values: map[string]any{"mode": "on-request"}}},
    },
    Prompt: "Diagnose the active incident; produce a rollback plan and summary.",
}
```

---

## 2. `scheduled-review` — 定期 PR / 文档 review

适用：cron job 每天 / 每小时跑一次 review，固定挂同一组 reviewer + 项目级指令。

```go
return Recipe{
    Name: "scheduled-review", Description: "Recurring code / docs review.",
    Trigger: "default (binding-level)",
    Resources: agentadaptor.ProfileResources{
        Skills: []agentadaptor.SkillRef{agentadaptor.Key("repo-map"), agentadaptor.Key("style-checker")},
        Agents: []agentadaptor.AgentSpec{{Key: "reviewer", RuntimeName: "reviewer", Instructions: "Follow team's review checklist."}},
        Instructions: &agentadaptor.InstructionsBundleRef{ID: "review-checklist", Path: filepath.Join(dir, "review-checklist.md"), Scope: agentadaptor.InstructionScopeProject, Mode: agentadaptor.InstructionModeAdditive},
    },
    Prompt: "Review the changes since last run; flag inconsistencies.",
}
```

---

## 3. `data-migration` — 数据迁移任务

适用：DB schema 升级、批数据搬运。要求 dry-run hooks + 严沙箱 + 全 transcript 审计。

```go
return Recipe{
    Name: "data-migration", Description: "Schema / data migration with dry-run gates.",
    Trigger: "per-run via WithProfileResources",
    Resources: agentadaptor.ProfileResources{
        Skills: []agentadaptor.SkillRef{agentadaptor.Key("schema-validator"), agentadaptor.Key("dry-run-runner")},
        Hooks: []agentadaptor.HookSpec{
            {Key: "pre-tool-dryrun", Event: agentadaptor.HookEventPreTool, MatcherSpec: agentadaptor.HookMatcher{Subject: agentadaptor.HookMatcherSubjectTool, Pattern: "sql"}, Handler: agentadaptor.HookHandler{Type: agentadaptor.HookHandlerCommand, Command: "dryrun-gate"}},
        },
        Config: []agentadaptor.ProfileConfigPatch{{Key: "sandbox", Capability: "sandbox", Values: map[string]any{"mode": "read_only"}}},
    },
    Prompt: "Plan the migration. Run dry-run only; do not commit.",
}
```

---

## 4. `security-scan` — 安全扫描

适用：夜间 / 周期性安全审计，只读隔离，发现告警生成 issue。

```go
return Recipe{
    Name: "security-scan", Description: "Read-only security audit.",
    Trigger: "default (binding-level)",
    Resources: agentadaptor.ProfileResources{
        Skills: []agentadaptor.SkillRef{agentadaptor.Key("secret-scanner"), agentadaptor.Key("dep-cve-checker")},
        Agents: []agentadaptor.AgentSpec{{Key: "security-reviewer", RuntimeName: "security-reviewer", Instructions: "Flag secrets, unsafe shell, vulnerable deps."}},
        Hooks: []agentadaptor.HookSpec{{Key: "finding-to-issue", Event: agentadaptor.HookEventStop, Handler: agentadaptor.HookHandler{Type: agentadaptor.HookHandlerHTTP, Command: "https://issues.example/api"}}},
        Config: []agentadaptor.ProfileConfigPatch{{Key: "sandbox", Capability: "sandbox", Values: map[string]any{"mode": "read_only"}}},
    },
    Prompt: "Scan the workspace; report any security finding.",
}
```

---

## 5. `customer-triage` — 客服分流

适用：客服坐席助手；客户消息进来先分流 → 找答案模板 → 确认后回复。

```go
return Recipe{
    Name: "customer-triage", Description: "Inbound message triage with reply templates.",
    Trigger: "per-run via WithProfileResources",
    Resources: agentadaptor.ProfileResources{
        Skills: []agentadaptor.SkillRef{agentadaptor.Key("customer-context"), agentadaptor.Key("reply-templates")},
        Agents: []agentadaptor.AgentSpec{{Key: "triage", RuntimeName: "triage", Instructions: "Classify intent, choose template, draft reply."}},
        Instructions: &agentadaptor.InstructionsBundleRef{ID: "triage-tone", Path: filepath.Join(dir, "triage-tone.md"), Scope: agentadaptor.InstructionScopeRun},
    },
    Prompt: "Triage the customer message and draft a reply using the closest template.",
}
```

---

## 6. `nightly-report` — 夜间汇总

适用：批量任务跑完后产出总结报告（团队站会素材、KPI dashboard 数据）。

```go
return Recipe{
    Name: "nightly-report", Description: "Aggregate day's runs into a digest.",
    Trigger: "default (binding-level)",
    Resources: agentadaptor.ProfileResources{
        Skills: []agentadaptor.SkillRef{agentadaptor.Key("metrics-aggregator"), agentadaptor.Key("digest-writer")},
        Hooks:  []agentadaptor.HookSpec{{Key: "publish-digest", Event: agentadaptor.HookEventStop, Handler: agentadaptor.HookHandler{Type: agentadaptor.HookHandlerCommand, Command: "publish-digest"}}},
        Instructions: &agentadaptor.InstructionsBundleRef{ID: "digest-format", Path: filepath.Join(dir, "digest-format.md"), Scope: agentadaptor.InstructionScopeProject, Mode: agentadaptor.InstructionModeAdditive},
    },
    Prompt: "Summarize today's runs into the team digest format.",
}
```

---

## 通用建议

- **新增剧本只动 `recipes.go`**：核心字段都在 `Recipe` struct 里；`main.go` 不需要改动
- **`Trigger` 用作约定**：`"default (binding-level)"` vs `"per-run via WithProfileResources"`，影响渲染时的 `+` / `↻` 标记
- **`InstructionScope`**：`Project`（项目通用）/ `Run`（一次性）/ `User`（用户级）。多数 per-run 剧本用 `Run`
- **`Disabled: true` 的 hook**：声明但不真启用，便于 staging → production 灰度上线
- **`Native` / `SourceFingerprint`** 用于 escape hatch：当声明式字段无法表达时，把原生 provider config 直接挂上去（详见 `agentadaptor.AgentSpec` / `HookHandler` 文档）

加 6 条以上的剧本时，考虑把 `Recipes(...)` 拆到子文件（`recipes_review.go` / `recipes_ops.go`）或抽成 sub-package，让 PR diff 保持小颗粒。
