# Codex HTTP 401 一次性自动更新 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为已启用的 Codex 401 重登录凭证提供可持久化、一次性、可审计的服务器自动更新。

**Architecture:** 在 Token Recovery 服务前增加自动信号门，统一处理请求监控、巡检和 HTTP signal 路由的开关、HTTP 401、CPA Core 启用状态和重复信号限制。恢复任务新增不可清除的 `auto_attempted_at_ms`，前端从现有运行时元数据渲染该历史标记。

**Tech Stack:** Go、SQLite、React、TypeScript、Vitest、Docker Compose。

---

### Task 1: 持久化自动尝试标记

**Files:**
- Modify: `apps/manager-server/internal/model/token_recovery.go`
- Modify: `apps/manager-server/internal/repository/sqlite/migrate.go`
- Modify: `apps/manager-server/internal/repository/tokenrecovery/repository.go`
- Test: `apps/manager-server/internal/repository/tokenrecovery/repository_test.go`

- [x] **Step 1: Write failing repository tests**

Add tests proving a completed automatic task remains terminal after a newer automatic signal, and that a later manual retry retains a non-zero `AutoAttemptedAtMS`.

- [x] **Step 2: Run the focused repository test**

Run: `go test ./internal/repository/tokenrecovery -run 'TestSignalAutomatic.*AutoAttempt' -count=1`

Expected: FAIL because `AutoAttemptedAtMS` does not exist or automatic success is requeued.

- [x] **Step 3: Implement the schema and repository changes**

Add nullable `auto_attempted_at_ms`, expose it through `TokenRecoveryTask`, set it atomically when an automatic queued task is claimed to run (immediately before external acquisition starts), and never clear it in manual state changes. Treat legacy automatic terminal modes/statuses as already attempted.

- [x] **Step 4: Run focused repository tests**

Run: `go test ./internal/repository/tokenrecovery -count=1`

Expected: PASS.

### Task 2: Add the persisted policy and automatic signal gate

**Files:**
- Modify: `apps/manager-server/internal/config/config.go`
- Modify: `apps/manager-server/internal/model/automation_settings.go`
- Modify: `apps/manager-server/internal/service/automation/service.go`
- Create: `apps/manager-server/internal/service/tokenrecovery/automatic_signal.go`
- Test: `apps/manager-server/internal/service/automation/service_test.go`
- Test: `apps/manager-server/internal/service/tokenrecovery/automatic_signal_test.go`

- [x] **Step 1: Write failing tests**

Add tests for a default-disabled policy, persisted enable patch, and automatic signal gate behavior: policy disabled skips the sink; enabled + CPA credential disabled skips the sink; enabled + current CPA credential enabled forwards exactly once.

- [x] **Step 2: Run focused tests**

Run: `go test ./internal/service/automation ./internal/service/tokenrecovery -run 'Test.*(CodexReauthAutoUpdate|AutomaticSignal)' -count=1`

Expected: FAIL because the new policy and gate do not exist.

- [x] **Step 3: Implement policy and gate**

Add config/env and DB-backed policy state. The gate reads existing recovery state before CPA Core verification, verifies current identity/status only for first automatic eligibility, and delegates only enabled identities to `SignalAutomatic`.

- [x] **Step 4: Run focused tests**

Run: `go test ./internal/service/automation ./internal/service/tokenrecovery -count=1`

Expected: PASS.

### Task 3: Route all automatic sources through the gate

**Files:**
- Modify: `apps/manager-server/internal/app/context.go`
- Modify: `apps/manager-server/cmd/cpa-manager-plus/main.go`
- Modify: `apps/manager-server/internal/http/controller/tokenrecovery/handler.go`
- Modify: `apps/manager-server/internal/worker/token_recovery_signal.go`
- Modify: `apps/manager-server/internal/worker/token_recovery_signal_test.go`
- Modify: `apps/manager-server/internal/service/codexinspection/service.go`
- Modify: `apps/manager-server/internal/service/codexinspection/token_recovery_test.go`

- [x] **Step 1: Write failing signal-source tests**

Require monitoring signals to reject 403 and ordinary 401, accept only classified Codex 401 reauth; require inspection signals to reject non-401 reauth results.

- [x] **Step 2: Run focused signal tests**

Run: `go test ./internal/worker ./internal/service/codexinspection -run 'Test.*(TokenRecovery|Reauth)' -count=1`

Expected: FAIL because non-401 signals are currently accepted.

- [x] **Step 3: Wire the gate and filter sources**

Construct one gate in app context; use it for inspection, collector fanout, and `/signals`. Keep `/manual` directly on the recovery service so operators can retry regardless of automatic history.

- [x] **Step 4: Run focused signal tests**

Run: `go test ./internal/worker ./internal/service/codexinspection ./internal/http/controller/tokenrecovery -count=1`

Expected: PASS.

### Task 4: Expose the switch and history marker in the web UI

**Files:**
- Modify: `apps/web/src/services/api/usageService.ts`
- Modify: `apps/web/src/features/config/model/accountProcessingPolicyViewModel.ts`
- Modify: `apps/web/src/features/config/model/accountProcessingPolicyViewModel.test.ts`
- Modify: `apps/web/src/features/demo/demoFixtures.ts`
- Modify: `apps/web/src/features/accounts/model/credentialRuntimeMetadata.ts`
- Modify: `apps/web/src/features/accounts/model/credentialRuntimeMetadata.test.ts`
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`
- Modify: `apps/web/src/i18n/locales/ru.json`

- [x] **Step 1: Write failing TypeScript tests**

Add a view-model test asserting the new capability appears under authentication issues, and a recovery metadata test asserting `autoAttemptedAtMs` renders a persistent automatic-attempt marker while retaining failure reason behavior.

- [x] **Step 2: Run focused web tests**

Run: `pnpm vitest run src/features/config/model/accountProcessingPolicyViewModel.test.ts src/features/accounts/model/credentialRuntimeMetadata.test.ts`

Expected: FAIL because the API type and presentation are absent.

- [x] **Step 3: Implement API/UI presentation**

Add policy field/patch mapping, capability card metadata, translations, task timestamp type, and a compact marker rendered next to existing recovery state on both list layouts.

- [x] **Step 4: Run focused web tests**

Run: `pnpm vitest run src/features/config/model/accountProcessingPolicyViewModel.test.ts src/features/accounts/model/credentialRuntimeMetadata.test.ts`

Expected: PASS.

### Task 5: Verify, commit, push only the fork, and deploy

**Files:**
- Modify: `C:\kyon\docker\cpa-manager\compose.yaml` only for version/source commit after build succeeds.

- [x] **Step 1: Run backend checks**

Run: `go test ./...`

- [x] **Step 2: Run web checks**

Run: `pnpm vitest run <affected tests>`, `pnpm typecheck`, `pnpm eslint <affected files>`, and the production build command used by this repository.

- [ ] **Step 3: Commit and push fork only**

Run: `git push origin lo/custom`; never push `upstream`.

- [ ] **Step 4: Build/deploy and verify health**

Back up compose, update only the local version/source commit label, run Docker Compose build/up, then verify `http://127.0.0.1:18317/health` returns HTTP 200 and inspect the running image label/version.
