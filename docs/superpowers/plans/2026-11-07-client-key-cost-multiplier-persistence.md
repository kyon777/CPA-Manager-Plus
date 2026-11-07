# 客户端 Key 成本倍率本地持久化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use inline TDD. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** 将客户端 Key 排行表倍率设置持久化到当前浏览器的 Usage Analytics UI state，默认 `1`，且不触及计费或后端。

**Architecture:** 扩展 `usageAnalyticsUiState.ts` 的本地 UI-state schema，增加经校验的 `apiKeyCostMultiplier` 数值字段。页面从该值初始化输入，输入/失焦时把归一化后的值写回同一 state；`RankTable` 继续只接收展示倍率，不修改原始成本数据。

**Tech Stack:** React 19、TypeScript、Vitest、react-test-renderer、browser localStorage。

---

### Task 1: 锁定 UI-state 读取/写入契约（RED）

**Files:**
- Modify: `apps/web/src/features/usage-analytics/usageAnalyticsUiState.test.ts`

- [x] 新增测试：空 storage 读取 `apiKeyCostMultiplier === 1`；带 `apiKeyCostMultiplier: 0.3` 的已存 JSON 可被读取；负数、非法字符串和无效 JSON 回退 `1`。
- [x] 运行：`npm --workspace apps/web run test -- src/features/usage-analytics/usageAnalyticsUiState.test.ts --testNamePattern="multiplier"`。
- [x] 确认修改前因缺少该字段而失败。

### Task 2: 扩展本地 UI-state schema（GREEN）

**Files:**
- Modify: `apps/web/src/features/usage-analytics/usageAnalyticsUiState.ts`
- Test: `apps/web/src/features/usage-analytics/usageAnalyticsUiState.test.ts`

- [x] 在 `UsageAnalyticsUiState` 和 patch 类型中增加 `apiKeyCostMultiplier`；默认/归一化逻辑始终返回非负有限数，默认 `1`。
- [x] 让 `writeUsageAnalyticsUiState` 合并并保持该字段；storage 不可用时保持现有静默回退。
- [x] 重新运行 Task 1 测试并确认通过。

### Task 3: 锁定页面重挂载持久化行为（RED）

**Files:**
- Modify: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.test.tsx`

- [x] 新增测试：预置 `0.3` 后页面输入显示 `0.3`；编辑倍率会写入 storage；卸载并重新创建页面后仍显示 `0.3`；不调用 `usage.refresh`，不修改原始行成本。
- [x] 运行：`npm --workspace apps/web run test -- src/features/usage-analytics/UsageAnalyticsPage.test.tsx --testNamePattern="persists.*multiplier"`。
- [x] 确认修改前失败，因为页面总以 `useState('1')` 初始化且不会写 storage。

### Task 4: 接入页面输入与持久化（GREEN）

**Files:**
- Modify: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.tsx`
- Test: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.test.tsx`

- [x] 从 UI state 读取初始倍率；在输入变更和失焦归一化时写回该值。
- [x] 保持请求、URL、筛选、排序和 `row.estimatedCost` 完全不变。
- [x] 重新运行 Task 3 测试并确认通过。

### Task 5: 回归、记忆与提交

**Files:**
- Modify: 两份本次 design/plan 文档。

- [x] 运行 Usage Analytics 测试、type-check、聚焦 lint 和生产构建。
- [x] 审计 diff，确认没有服务端、数据库、CPA Core 或计费文件。
- [x] 提交本功能与文档；不部署，除非用户明确要求部署。
