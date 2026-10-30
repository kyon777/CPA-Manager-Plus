# 凭证恢复状态与写入超时收敛 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让当前页恢复状态稳定地从持久化任务读取，避免历史成功状态触发重复凭证重载，并在 CPA 上传超时而实际写入已完成时安全地收敛为成功。

**Architecture:** Manager 的 Recovery Repository 新增只读批量快照接口，复用精确身份和受限显示兜底规则；HTTP Handler 用一次服务调用填充当前页项目。Web Hook 记录仅本会话内观察到的 pending 任务，Accounts 页面只在 pending 到成功的转变时刷新对应凭证。上传超时则在释放写入锁之后使用内存结果做有限次只读验证，绝不重发 Token Acquisition 或上传。

**Tech Stack:** Go 1.26、SQLite/modernc、React 19、TypeScript、Vitest、Go testing。

---

### Task 1: Repository 批量状态快照与安全显示兜底

**Files:**
- Modify: `apps/manager-server/internal/repository/tokenrecovery/repository.go`
- Modify: `apps/manager-server/internal/repository/tokenrecovery/repository_test.go`
- Modify: `apps/manager-server/internal/store/store.go`
- Modify: `apps/manager-server/internal/service/tokenrecovery/service.go`
- Test: `apps/manager-server/internal/repository/tokenrecovery/repository_test.go`

- [ ] **Step 1: 写失败测试，定义唯一邮箱显示兜底和歧义关闭**

```go
func TestGetBatchForDisplayFindsUniqueStaleAuthIndexByEmail(t *testing.T) {
    repo := newTestRepository(t)
    // 创建 old-index 的持久任务；当前目标提供 same file/provider/email 与 new-index。
    // 期望 batch 返回 old task。
}

func TestGetBatchForDisplayDoesNotFallbackWhenEmailIsAmbiguous(t *testing.T) {
    repo := newTestRepository(t)
    // 同一个 file/provider/email 写入两个不同 auth_index 任务。
    // 期望 stale-index target 不返回任务。
}
```

- [ ] **Step 2: 运行失败测试，确认 API 尚不存在**

Run: `go test ./internal/repository/tokenrecovery -run 'TestGetBatchForDisplay' -count=1`

Expected: FAIL，原因是 `GetBatchForDisplay` 尚未定义。

- [ ] **Step 3: 增加批量只读 API**

在 repository、Store facade 和 recovery TaskStore/Service 中定义：

```go
GetTokenRecoveryBatchForDisplay(context.Context, []store.TokenRecoveryTarget) ([]store.TokenRecoveryTask, error)
```

实际 repository 方法按输入顺序返回同长度任务切片，其中零值任务表示无可显示项目。实现采用一次 SQLite 查询读取与当前页关联的 `file_name/provider` 任务行，再在内存建立：

```go
type displayRecoveryIndex struct {
    exact map[string]model.TokenRecoveryTask
    byAuthIndex map[string]model.TokenRecoveryTask
    byEmail map[string]uniqueDisplayTask // task + ambiguous flag
}
```

匹配严格顺序：完整 `identity_key`、同 `file_name/provider/auth_index`、仅唯一的 `file_name/provider/account_email`。最后一层只给显示调用，不能改变 `Get`、`SignalAutomatic`、`RequestManual` 的队列语义。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/repository/tokenrecovery -run 'Test(GetBatchForDisplay|GetFindsEmailOnlyTaskAfterAuthIndexBecomesAvailable)' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交独立仓储改动**

```bash
git add apps/manager-server/internal/repository/tokenrecovery/repository.go \
  apps/manager-server/internal/repository/tokenrecovery/repository_test.go \
  apps/manager-server/internal/store/store.go \
  apps/manager-server/internal/service/tokenrecovery/service.go
git commit -m "perf: batch durable recovery state lookups"
```

### Task 2: Handler 改用单个批量服务读取

**Files:**
- Modify: `apps/manager-server/internal/http/controller/tokenrecovery/handler.go`
- Modify: `apps/manager-server/internal/http/controller/tokenrecovery/handler_test.go`
- Test: `apps/manager-server/internal/http/controller/tokenrecovery/handler_test.go`

- [ ] **Step 1: 写失败 Handler 测试**

```go
func TestHandlerBatchQueryUsesDisplayFallbackForAStaleAuthIndex(t *testing.T) {
    handler := newTestHandler(t)
    // 先创建 old-index 任务，再请求 new-index 但同 file/email 的 target。
    // 断言返回 task 而非 null。
}
```

- [ ] **Step 2: 运行失败测试**

Run: `go test ./internal/http/controller/tokenrecovery -run TestHandlerBatchQueryUsesDisplayFallbackForAStaleAuthIndex -count=1`

Expected: FAIL，因为 Handler 仍逐个调用严格 `Get`。

- [ ] **Step 3: 最小 Handler 替换**

在 `/v0/management/token-recovery/query` 分支中：

```go
targets := make([]model.TokenRecoveryTarget, len(batchTargets))
for index, target := range batchTargets { targets[index] = target.Target }
tasks, err := h.App.TokenRecoveryService.GetBatchForDisplay(r.Context(), targets)
```

一次错误时将每个 clientKey 标为同一个受控 `errorCode`；成功时按索引填充非零 `task.ID`。手动批量队列逻辑保持不变。

- [ ] **Step 4: 运行 Handler 相关测试**

Run: `go test ./internal/http/controller/tokenrecovery -count=1`

Expected: PASS。

- [ ] **Step 5: 提交 Handler 改动**

```bash
git add apps/manager-server/internal/http/controller/tokenrecovery/handler.go \
  apps/manager-server/internal/http/controller/tokenrecovery/handler_test.go
git commit -m "perf: batch recovery state endpoint reads"
```

### Task 3: CPA 上传超时后的只读结果复核

**Files:**
- Modify: `apps/manager-server/internal/service/tokenrecovery/service.go`
- Modify: `apps/manager-server/internal/service/tokenrecovery/service_test.go`
- Test: `apps/manager-server/internal/service/tokenrecovery/service_test.go`

- [ ] **Step 1: 写失败测试：上传超时但后端写入完成**

```go
func TestServiceCompletesWhenTimedOutUploadWasApplied(t *testing.T) {
    files := &appliedThenTimeoutRecoveryAuthFiles{...}
    // Upload 保存 merged 内容再返回 context.DeadlineExceeded。
    // 断言最终 succeeded，Acquire 和 Upload 各一次。
}

func TestServiceKeepsTimeoutWhenTimedOutUploadCannotBeVerified(t *testing.T) {
    files := &timeoutOnUploadRecoveryAuthFiles{...}
    // 断言仍为 auto_failed_manual_only/cpa_auth_file_timeout，且无第二次 Upload/Acquire。
}
```

- [ ] **Step 2: 运行失败测试**

Run: `go test ./internal/service/tokenrecovery -run 'TestService(CompletesWhenTimedOutUploadWasApplied|KeepsTimeoutWhenTimedOutUploadCannotBeVerified)' -count=1`

Expected: 首个测试 FAIL，当前实现把上传 timeout 直接终态失败。

- [ ] **Step 3: 加入受限只读复核**

增加专用常量与函数：

```go
const timedOutUploadVerificationAttempts = 3
const timedOutUploadVerificationTimeout = 10 * time.Second

func (s *Service) reconcileTimedOutUpload(
    lifecycleCtx context.Context,
    setup store.Setup,
    fileName string,
    locator Locator,
    result AcquisitionResult,
) bool
```

`writeCurrentCredential` 将上传 timeout 保留为可识别错误，让 `recover` 在写入方法返回后调用该函数。复核 context 必须从 `context.WithoutCancel(lifecycleCtx)` 创建且总时长有上限；每次仅 `Download` 与 `VerifyMergedAuthJSON`。成功即返回 `nil`，否则返回原始上传 timeout。不得调用 `Acquire`、`Upload`，不得扩大 write gate，也不得在普通 HTTP 上传失败时进入复核。

- [ ] **Step 4: 运行恢复服务全套测试**

Run: `go test ./internal/service/tokenrecovery -count=1`

Expected: PASS。

- [ ] **Step 5: 提交超时收敛改动**

```bash
git add apps/manager-server/internal/service/tokenrecovery/service.go \
  apps/manager-server/internal/service/tokenrecovery/service_test.go
git commit -m "fix: reconcile timed out credential uploads"
```

### Task 4: 前端仅在会话中 pending 转成功时刷新凭证

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx`
- Test: `apps/web/src/features/accounts/AccountsPage.test.tsx`

- [ ] **Step 1: 写失败组件测试**

```tsx
it('does not reload credentials for a historical succeeded recovery task', async () => {
  // 初次渲染就提供 succeeded recoveryTask。
  // 断言 loadFiles 未因该任务额外调用。
});

it('reloads the credential once after an observed pending task succeeds', async () => {
  // 先渲染 manual_running，再更新同 task ID 为 succeeded。
  // 断言 target 物理文件只触发一次恢复后的 reload。
});
```

- [ ] **Step 2: 运行失败测试**

Run: `npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx -t "historical succeeded|observed pending"`

Expected: 历史成功测试 FAIL，因为现有 effect 会对每个 `succeeded` 直接执行重载。

- [ ] **Step 3: 记录会话内 pending 观察状态**

在 `AccountsPage.tsx` 为当前 `managerConnectionFingerprint + clientKey + task.ID` 保存已观察 pending 的集合。完成 effect 的规则改为：

```ts
if (isPendingRecoveryStatus(task.status)) observedPending.add(key)
if (task.status === 'succeeded' && observedPending.delete(key)) {
  void handleServerTokenRecoverySuccessForTarget(candidate)
}
```

连接指纹变更时清理集合和完成去重集合。保留既有服务端任务标签渲染、错误状态和按 2 秒轮询 pending 任务的行为。

- [ ] **Step 4: 运行 Web 定向测试**

Run: `npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx src/features/accounts/hooks/useCredentialRuntimeMetadata.test.tsx`

Expected: PASS。

- [ ] **Step 5: 提交前端改动**

```bash
git add apps/web/src/features/accounts/AccountsPage.tsx \
  apps/web/src/features/accounts/AccountsPage.test.tsx
git commit -m "fix: avoid reloading historical recovery successes"
```

### Task 5: 二改记忆、全量验证与部署候选

**Files:**
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`
- Test: affected Go/Vitest suites, TypeScript type check, production build

- [ ] **Step 1: 更新二改记忆**

在 `docs/LO-CPAM-CUSTOM-MEMORY.md` 新增 2026-10-30 小节，明确：恢复状态来自持久化 `token_recovery_tasks`；普通列表只查当前页；唯一 `(file, provider, email)` 兜底只用于显示；历史成功不得触发凭证 reload；上传 timeout 仅做有限只读复核，不重取 Token、不重传、不改 8 路写入限制。

- [ ] **Step 2: Go 验证**

Run: `cd apps/manager-server && go test ./internal/repository/tokenrecovery ./internal/service/tokenrecovery ./internal/http/controller/tokenrecovery`

Expected: PASS。

- [ ] **Step 3: Web 验证**

Run: `npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx src/features/accounts/hooks/useCredentialRuntimeMetadata.test.tsx src/features/accounts/model/credentialRuntimeMetadata.test.ts && npm run type-check && npm run build`

Expected: PASS。

- [ ] **Step 4: 差异与提交核验**

Run: `git diff ee9629bf..HEAD --check && git status --short`

Expected: 无空白错误；仅本任务文件变更且工作树干净。

- [ ] **Step 5: 提交记忆与验证后的候选版本**

```bash
git add docs/LO-CPAM-CUSTOM-MEMORY.md
git commit -m "docs: preserve recovery timeout reconciliation"
```

部署前必须按项目约定确认本地 CPA Core 容器与服务器 `cpa.service` 身份；只构建、部署和重启 CPAM。
