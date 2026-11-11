# 巡检全量重新登录与确认封禁删除 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在本地 Codex 巡检已完成的“重新登录”结果中，安全地支持全量提交 Token Acquisition 人工恢复，以及仅删除已被 durable task 确认 `account_banned` 的物理凭证文件，并在验证通过后直接双端部署 CPAM。

**Architecture:** 浏览器在点击全量按钮时冻结当前巡检快照，以 100 项为界顺序查询 Manager durable recovery task；恢复流程只提交状态明确可重试的项，删除流程只提交同一物理文件内所有冻结成员均已确认封禁的完整组。纯分段、预检、分组和累计结果规则放入既有 `inspectionPageBatchOperation` 模型；页面只负责编排 API、连接世代保护和本地结果收敛，结果面板只渲染按钮/进度。

**Tech Stack:** React 19、TypeScript、Vitest、react-test-renderer、i18next、Axios usage-service API、Docker Linux/amd64 CPAM candidate、PowerShell guarded local promotion、LACPA-VPS 原生 `cpam.service`。

---

## 固定边界

- 仅改 CPAM 前端和 CPAM 文档；绝不改、构建、重启、部署 CPA Core / `cli-proxy-api`。
- 目标集合始终是点击瞬间 `result.results.filter(isReauthAction)` 的本地已完成巡检快照，不是当前分页、不是后续导入账号、不是全量凭证库存。
- 全量预检只在按钮点击时触发。首屏、翻页和筛选切换绝不扫描全量 `token-recovery/query`。
- 每个浏览器到 Manager 的恢复查询、恢复提交、封禁删除请求最多 100 项，外层顺序执行。
- Manager 的 Token Acquisition 40 路门、CPA 文件写入 8 路门、封禁删除的服务端严格身份/共享物理文件检查不变。
- 本地页不再传入旧的 `onDeleteReauthPlanned`。该旧按钮会直接按“需要重新登录”删除，与“只删已确认封禁”冲突；服务器巡检页保留原有 prop 和行为。
- 用户已明确要求：所有实现与验证通过后直接部署；若出现真实验证/保护守卫阻断，停止在阻断点并报告，不触碰 CPA Core。

## 文件结构

| 路径 | 责任 |
| --- | --- |
| `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.ts` | 100 项分段、恢复 task 预检分类、完整物理文件封禁分组、累计删除结果、可展示的操作进度纯函数。 |
| `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.test.ts` | 纯模型的边界、幂等、分组和累计结果回归测试。 |
| `apps/web/src/features/monitoring/CodexInspectionPage.tsx` | 冻结巡检快照、顺序 API 编排、连接指纹/世代保护、双确认、局部结果收敛。 |
| `apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx` | 本地页的全量恢复/删除 API 时序、无首屏全扫、切换连接丢弃旧响应测试。 |
| `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx` | 仅在本地 reauth 筛选显示两个全量按钮、禁用状态与进度反馈。 |
| `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx` | 全量按钮的筛选可见性、数量、繁忙禁用和旧删除入口兼容测试。 |
| `apps/web/src/i18n/locales/{en,ru,zh-CN,zh-TW}.json` | 四语言文案。 |
| `docs/LO-CPAM-CUSTOM-MEMORY.md` | 持久化本次二改的范围、幂等、安全和部署约束。 |
| `docs/superpowers/specs/2026-11-11-inspection-global-recovery-and-banned-delete-design.md` | 已确认设计，保持不改业务含义。 |

### Task 1: 建立可验证基线与依赖

**Files:**
- Modify: none
- Test: existing focused web tests and Manager Go test suite

- [ ] **Step 1: 在隔离 worktree 安装锁定依赖，不触碰 `source` WIP**

Run:

```powershell
$wt = 'C:\Users\Administrator\.config\superpowers\worktrees\cpa-manager\inspection-global-actions-20261111'
Set-Location $wt
npm ci
```

Expected: 根目录生成未跟踪的 `node_modules`（被 `.gitignore` 忽略）；`git status --short` 不出现源码改动。

- [ ] **Step 2: 运行当前功能的针对性基线**

Run:

```powershell
$env:TZ = 'Asia/Shanghai'
npm --workspace apps/web run test -- src/features/monitoring/model/inspectionPageBatchOperation.test.ts src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx --testTimeout=20000
Push-Location apps/manager-server
go test -count=1 ./internal/service/authfilesbanneddelete ./internal/http/controller/tokenrecovery
Pop-Location
```

Expected: 当前三个 web 文件和两个 Manager 包通过。若基线失败，先记录精确失败；不得把新功能写在未知失败基线上。

### Task 2: 为快照批量决策建立纯模型（TDD）

**Files:**
- Modify: `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.ts`
- Test: `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.test.ts`

- [ ] **Step 1: 写出失败的分段、预检和物理文件完整性测试**

在测试文件新增最小 fixture：

```ts
const snapshotTarget = (clientKey: string, fileName = `${clientKey}.json`) => ({
  recoveryTarget: {
    clientKey,
    fileName,
    authIndex: `index-${clientKey}`,
    accountEmail: `${clientKey}@example.com`,
    provider: 'codex' as const,
  },
  deletionSource: {
    clientKey,
    fileName,
    authIndex: `index-${clientKey}`,
    accountEmail: `${clientKey}@example.com`,
    provider: 'codex',
    runtimeId: `runtime-${clientKey}`,
    accountSnapshot: `${clientKey}@example.com`,
  },
});
```

新增断言覆盖：

```ts
expect(chunkInspectionSnapshotTargets(Array.from({ length: 201 }, (_, i) => i)))
  .toEqual([expect.arrayContaining(Array(100)), expect.arrayContaining(Array(100)), [200]]);

expect(planInspectionSnapshotRecovery({
  candidates: [snapshotTarget('new'), snapshotTarget('queued'), snapshotTarget('done'), snapshotTarget('banned')],
  taskItemsByClientKey: new Map([
    ['new', { clientKey: 'new', task: null }],
    ['queued', { clientKey: 'queued', task: queuedTask }],
    ['done', { clientKey: 'done', task: succeededAfterInspection }],
    ['banned', { clientKey: 'banned', task: accountBannedTask }],
  ]),
  inspectionFinishedAtMs: 10_000,
}).submitTargets.map((item) => item.clientKey)).toEqual(['new']);
```

再新增封禁删除计划测试：同名 `shared.json` 的两个成员只要有一个 `not_account_banned` 就整个文件不进 batch；101 个同文件成员整组跳过；99 项组加 2 项组必须形成 `[99]`、`[2]` 两个 batch，绝不拆分共享物理文件。

- [ ] **Step 2: 运行测试确认失败原因是缺少新导出**

Run:

```powershell
$env:TZ = 'Asia/Shanghai'
npm --workspace apps/web run test -- src/features/monitoring/model/inspectionPageBatchOperation.test.ts --testTimeout=20000
```

Expected: FAIL，提示 `chunkInspectionSnapshotTargets`、`planInspectionSnapshotRecovery` 或 `planInspectionSnapshotAccountBannedDelete` 尚未导出。

- [ ] **Step 3: 实现最小纯模型，不引入网络或 React 状态**

在 `inspectionPageBatchOperation.ts` 添加以下稳定边界：

```ts
export const INSPECTION_SNAPSHOT_BATCH_LIMIT = 100;

export type InspectionSnapshotTarget = {
  recoveryTarget: TokenRecoveryBatchTargetRequest;
  deletionSource: AccountBannedDeleteSource;
};

export type InspectionSnapshotSkip = { clientKey: string; reason: string };

export const chunkInspectionSnapshotTargets = <T>(
  targets: readonly T[],
  limit = INSPECTION_SNAPSHOT_BATCH_LIMIT
): T[][] => {
  const chunks: T[][] = [];
  for (let offset = 0; offset < targets.length; offset += limit) {
    chunks.push(targets.slice(offset, offset + limit));
  }
  return chunks;
};
```

`planInspectionSnapshotRecovery` 必须使用 `isPendingRecoveryStatus`、
`isTerminalAccountBannedRecoveryTask` 和 `completedAtMs > inspectionFinishedAtMs` 分类：

- `task === null`、普通终态失败、或早于本轮巡检的成功任务 → `submitTargets`；
- queued/running、巡检后成功、`account_banned`、缺失 response item → `skipped`；
- 每个跳过理由为稳定短码，如 `recovery_pending`、`recovery_succeeded`、`account_banned`、`recovery_task_unavailable`。

`planInspectionSnapshotAccountBannedDelete` 必须先以 `fileName.trim().toLowerCase()` 分组（与
Manager `normalizedPhysicalName` 一致），对每个成员调用既有
`buildAccountBannedDeleteTarget(source, task)`。只有**组内每一项**都生成严格 target 时才可
进入贪心 100 项 batch；不完整组记录 `shared_file_incomplete`，超过 100 的组记录
`shared_file_too_large`。实现 `mergeInspectionPageDeleteOperationResults`，用既有
`reconcileInspectionPageDeleteResponse` 累加 `requested/deleted/alreadyAbsent/skipped/failed` 并只保留
`maxDeleteDetails` 条有界 detail。

- [ ] **Step 4: 运行模型测试确认通过**

Run:

```powershell
$env:TZ = 'Asia/Shanghai'
npm --workspace apps/web run test -- src/features/monitoring/model/inspectionPageBatchOperation.test.ts --testTimeout=20000
```

Expected: PASS；201 项分段、状态分类、共享文件完整性、101 项拒绝和累计删除结果均通过。

- [ ] **Step 5: 提交纯模型边界**

```powershell
git add apps/web/src/features/monitoring/model/inspectionPageBatchOperation.ts apps/web/src/features/monitoring/model/inspectionPageBatchOperation.test.ts
git commit -m "feat: plan inspection snapshot batches"
```

### Task 3: 渲染本地巡检的两个全量操作入口（TDD）

**Files:**
- Modify: `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx`
- Modify: `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/ru.json`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`

- [ ] **Step 1: 写出失败的结果面板测试**

在现有“current-page recovery and banned-delete actions only in the reauth filter”测试旁添加：

```tsx
const renderer = renderPanel(createItem({ action: 'reauth' }), {
  actionFilter: 'reauth',
  snapshotRecoveryCount: 1239,
  snapshotActionBusy: false,
  onRecoverSnapshot: vi.fn(),
  onDeleteSnapshotBanned: vi.fn(),
});

expect(renderer.root.findByProps({ 'data-inspection-snapshot-recovery': true }).props.disabled)
  .toBe(false);
expect(renderer.root.findByProps({ 'data-inspection-snapshot-banned-delete': true }).props.disabled)
  .toBe(false);
```

再断言：非 `reauth` 筛选找不到两个 data 属性；`snapshotActionBusy=true` 时当前页和全量四个
恢复/删除按钮均 disabled；没有 `onDeleteReauthPlanned` 时不渲染旧的“删除需重新登录账号”按钮。

- [ ] **Step 2: 运行组件测试确认失败**

Run:

```powershell
$env:TZ = 'Asia/Shanghai'
npm --workspace apps/web run test -- src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx --testTimeout=20000
```

Expected: FAIL，props/data attributes 尚不存在。

- [ ] **Step 3: 扩展可选 props 与工具栏，保持服务器页兼容**

在 `CodexInspectionResultsPanelProps` 增加：

```ts
snapshotRecoveryCount?: number;
snapshotActionBusy?: boolean;
snapshotRecoveryLabel?: string;
snapshotDeleteLabel?: string;
snapshotFeedback?: ReactNode;
onRecoverSnapshot?: () => void;
onDeleteSnapshotBanned?: () => void;
```

仅在 `actionFilter === 'reauth'` 且对应回调存在时渲染：

```tsx
<Button
  data-inspection-snapshot-recovery={true}
  onClick={onRecoverSnapshot}
  disabled={!result || isInspectionInFlight || executing || snapshotActionBusy || snapshotRecoveryCount === 0}
>
  <IconRefreshCw size={14} />
  {snapshotRecoveryLabel ?? t('accounts.batch_recovery_snapshot', { count: snapshotRecoveryCount })}
</Button>
<Button
  variant="danger"
  data-inspection-snapshot-banned-delete={true}
  onClick={onDeleteSnapshotBanned}
  disabled={!result || isInspectionInFlight || executing || snapshotActionBusy || snapshotRecoveryCount === 0}
>
  <IconTrash2 size={14} />
  {snapshotDeleteLabel ?? t('accounts.batch_delete_banned_snapshot')}
</Button>
```

当前页恢复/删除 disabled 条件追加 `snapshotActionBusy`。把 `snapshotFeedback` 放进已有
`inspectionPageOperationFeedback` 容器，使用 `aria-live="polite"`。不移除通用
`onDeleteReauthPlanned` prop：服务器页仍可传它；本地页将在 Task 4 停止传入。

- [ ] **Step 4: 加入四语言文案并校验 JSON**

在每个 `accounts` locale 对象添加等价 key：

```json
{
  "batch_recovery_snapshot": "更新全部需重新登录凭证 ({{count}})",
  "batch_delete_banned_snapshot": "删除所有已确认封禁账号",
  "inspection_snapshot_querying": "核验恢复状态 {{completed}}/{{total}}",
  "inspection_snapshot_submitting": "已提交 {{submitted}}/{{eligible}}；跳过 {{skipped}}",
  "inspection_snapshot_deleting": "正在删除已确认封禁账号 {{completed}}/{{total}}",
  "inspection_snapshot_no_banned": "当前巡检快照没有已确认封禁账号",
  "inspection_snapshot_preflight_failed": "恢复状态核验未完成，未提交未知凭证"
}
```

四个 JSON 均通过 `ConvertFrom-Json`；英语/俄语/繁体翻译采用相同语义，不能仅复制中文。

- [ ] **Step 5: 运行组件测试确认通过**

Run:

```powershell
$env:TZ = 'Asia/Shanghai'
npm --workspace apps/web run test -- src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx --testTimeout=20000
```

Expected: PASS；当前页按钮保留、全量按钮仅 local prop + reauth 显示、服务器旧 prop 不受影响。

- [ ] **Step 6: 提交组件和本地化**

```powershell
git add apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx apps/web/src/i18n/locales/en.json apps/web/src/i18n/locales/ru.json apps/web/src/i18n/locales/zh-CN.json apps/web/src/i18n/locales/zh-TW.json
git commit -m "feat: add inspection snapshot action controls"
```

### Task 4: 编排全量恢复与全量封禁删除（TDD）

**Files:**
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.tsx`
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx`

- [ ] **Step 1: 为 101 项全量恢复写失败生命周期测试**

新增 `reauthSnapshotLastRun(count)` fixture，以不同文件名、`authIndex`、邮箱生成 101 个
`isReauthAction` 行。测试按以下 API 时序断言：

```ts
expect(mocks.queryTokenRecoveryBatch).not.toHaveBeenCalled(); // 首屏稳定后
renderer.root.findByProps({ 'data-inspection-snapshot-recovery': true }).props.onClick();
await flushPromises();
expect(mocks.queryTokenRecoveryBatch).toHaveBeenNthCalledWith(1, managerBase, managerKey, expect.any(Array));
expect(mocks.queryTokenRecoveryBatch.mock.calls[0][2]).toHaveLength(100);
expect(mocks.queryTokenRecoveryBatch.mock.calls[1][2]).toHaveLength(1);
expect(mocks.requestTokenRecoveryManualBatch.mock.calls[0][2]).toHaveLength(100);
expect(mocks.requestTokenRecoveryManualBatch.mock.calls[1][2]).toHaveLength(1);
```

在同一测试中让四项分别为 `manual_queued`、巡检结束后 `succeeded`、
`auto_failed_manual_only + account_banned`、`task:null`，断言前三项不进入 manual batch，最后一项
进入。另写预检请求 reject 的测试，断言 `requestTokenRecoveryManualBatch` 为零次。

- [ ] **Step 2: 为全量删除写失败生命周期测试**

使用 102 项结果，构造：两个 `shared.json` 成员，其中一个非封禁；100 项互不共享且确认封禁；
一个无 task。断言：

```ts
renderer.root.findByProps({ 'data-inspection-snapshot-banned-delete': true }).props.onClick();
await flushPromises();
expect(mocks.deleteAccountBannedAuthFilesBatch).not.toHaveBeenCalled();
expect(mocks.showConfirmation).toHaveBeenCalledTimes(1);
const config = mocks.showConfirmation.mock.calls[0][0];
expect(config.secondConfirmation).toBeDefined();
config.onConfirm(); // confirmation framework 的第二确认完成后才触发此 callback
await flushPromises();
expect(mocks.deleteAccountBannedAuthFilesBatch).toHaveBeenCalledTimes(1);
expect(mocks.deleteAccountBannedAuthFilesBatch.mock.calls[0][2]).toHaveLength(100);
expect(JSON.stringify(mocks.deleteAccountBannedAuthFilesBatch.mock.calls)).not.toContain('shared.json');
```

补充测试：切换 `apiBase/managementKey` 并 resolve 旧 query/deletion promise 后，旧 callback 不调用
`setResult`、`onAuthFilesDeleted` 或 notification；初始渲染/翻页不调用 101 项 query。

- [ ] **Step 3: 运行生命周期测试确认失败**

Run:

```powershell
$env:TZ = 'Asia/Shanghai'
npm --workspace apps/web run test -- src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx --testTimeout=20000
```

Expected: FAIL，因为全量 data attributes、分段请求和 snapshot 操作状态尚不存在。

- [ ] **Step 4: 在页面创建冻结 target 与共享世代保护**

在既有 `buildInspectionRecoveryTarget` 旁新增稳定的 snapshot candidate builder：

```ts
const buildInspectionSnapshotTarget = (item: CodexInspectionResultItem): InspectionSnapshotTarget | null => {
  const recoveryTarget = buildInspectionRecoveryTarget(item);
  if (!recoveryTarget) return null;
  return {
    recoveryTarget,
    deletionSource: {
      clientKey: item.key,
      fileName: item.fileName,
      authIndex: item.authIndex,
      accountEmail: inspectionAccountEmail(item),
      provider: item.provider,
      runtimeId: item.runtimeId,
      accountId: item.accountId,
      accountSnapshot: item.accountSnapshot,
    },
  };
};
```

以 `reauthResults`（不是 `resultPagination.pageItems`）构造去重的 snapshot target 列表。新增独立的
`inspectionSnapshotGenerationRef`、`inspectionSnapshotBusyRef`、恢复/删除 operation state；在现有连接
fingerprint reset effect 中递增世代并清空它们。每个 await 后都执行：

```ts
if (
  activeConnectionFingerprintRef.current !== requestConnectionFingerprint ||
  inspectionSnapshotGenerationRef.current !== requestGeneration
) return;
```

实现顺序 `querySnapshotRecoveryTasks`：使用 `chunkInspectionSnapshotTargets` 请求已有
`usageServiceApi.queryTokenRecoveryBatch`；每个批次必须返回每个 `clientKey` 的 item（`task:null` 合法），
否则返回“预检不完整”并禁止后续恢复/删除。该 helper 不写 localStorage，也不触发 CPA 请求。

- [ ] **Step 5: 实现全量恢复提交，不改变 worker 并发**

`handleRecoverSnapshot` 执行：冻结 target / `result.finishedAt` → 完整 sequential query →
`planInspectionSnapshotRecovery` → sequential `requestTokenRecoveryManualBatch`。每个 response 返回的 task
通过既有 `applyPageRuntimeBatchTasks(response.items.filter((item) => visibleRecoveryKeys.has(item.clientKey.trim())))` 合并可见页状态；不得把离屏 1239 项塞进当前页 runtime metadata map。逐项无 task/error 的项目累计失败但继续下一批；
HTTP/传输错误停止后续 batch、保留已确认提交计数。页面操作状态至少携带：

```ts
{
  kind: 'recovery',
  phase: 'querying' | 'submitting' | 'completed',
  total: frozenTargets.length,
  completed: queryOrSubmitCount,
  eligible: plan.submitTargets.length,
  submitted: durableSubmissionCount,
  skipped: plan.skipped.length,
  failed: submissionFailures.length,
  failureReasons: boundedReasons,
}
```

不把离屏成功行从旧巡检结果删除；可见页仍由既有 `selectPostInspectionManualSuccessKeys` 处理。这样全量提交不会变成全量轮询。

- [ ] **Step 6: 实现全量已确认封禁删除与逐批收敛**

`handleDeleteSnapshotBanned` 执行：冻结 target → 完整 sequential query →
`planInspectionSnapshotAccountBannedDelete`。无候选只设置 completed/no-banned 反馈，不调用确认框。
有候选时以一个 `showConfirmation` 配置提供 `secondConfirmation`：

```ts
showConfirmation({
  title: t('accounts.batch_delete_banned_snapshot_title', { count: plan.targetCount }),
  message: t('accounts.batch_delete_banned_snapshot_summary', {
    rows: plan.targetCount,
    files: plan.fileCount,
  }),
  confirmText: t('common.delete'),
  cancelText: t('common.cancel'),
  variant: 'danger',
  secondConfirmation: {
    title: t('auth_files.delete_many_second_title'),
    message: t('auth_files.delete_many_second_confirm', {
      count: plan.targetCount,
      scope: t('auth_files.delete_scope_selected'),
    }),
    confirmText: t('auth_files.delete_second_action'),
    cancelText: t('common.cancel'),
    variant: 'danger',
  },
  onConfirm: () => void deleteSnapshotBannedBatches(plan.batches, requestConnectionFingerprint),
});
```

`deleteSnapshotBannedBatches` 按 batch 顺序调用既有删除 API。每个成功 response 都用
`reconcileInspectionPageDeleteResponse` 立即从 `currentAuthFiles`、`result` 和
`onAuthFilesDeleted` 收敛已删除/`credential_not_found` 物理名；再用
`mergeInspectionPageDeleteOperationResults` 更新累计反馈。传输异常停止后续批次，不能凭浏览器预检自行删除。

最后在 `result.finishedAt > 0` 的本地已完成巡检中向 `CodexInspectionResultsPanel` 传入 `snapshotRecoveryCount`（显示完整 reauth snapshot 数）、共享 busy、两个新 callback、格式化进度 feedback；运行中巡检不传这两个回调。删除本地 `onDeleteReauthPlanned={...}` 和已无调用的
`handleDeleteReauthPlanned`/`toReauthDeleteExecutionItem`。不得删除服务器页的同名 prop。

- [ ] **Step 7: 运行生命周期测试确认通过**

Run:

```powershell
$env:TZ = 'Asia/Shanghai'
npm --workspace apps/web run test -- src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx --testTimeout=20000
```

Expected: PASS；101 项分段严格顺序、无首屏全扫、预检失败不提交、确认封禁才删、共享文件不拆分、连接切换丢弃旧响应。

- [ ] **Step 8: 提交页面编排**

```powershell
git add apps/web/src/features/monitoring/CodexInspectionPage.tsx apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx
git commit -m "feat: run global inspection recovery actions"
```

### Task 5: 写入二改记忆并完成前端回归

**Files:**
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`
- Test: focused web suite, type-check, production build, Manager suite

- [ ] **Step 1: 写入不可回退二改记忆**

在 `docs/LO-CPAM-CUSTOM-MEMORY.md` 的最新 2026-11-11 巡检条目后增加
“巡检结果全量重新登录与确认封禁删除（must retain）”，明确：

```markdown
- 本地已完成 Codex 巡检的 reauth 快照支持全量恢复和全量确认封禁删除；不是分页也不是全库存。
- query/manual/delete 均最多 100 项且浏览器外层顺序；首屏/翻页禁止全量 query。
- queued/running、巡检后成功、account_banned 不会再次被人工恢复提交；只有明确失败或无任务可再次提交。
- 删除必须先完整核验 terminal account_banned，再按不拆分共享物理文件的批次和同一份 secondConfirmation 执行；服务端严格身份/库存/锁仍是最终裁决。
- 仅 CPAM 前端；不改 CPA Core、cli-proxy-api、计费、额度、派生库、Token Acquisition 40 路或 CPA 写入 8 路门。
```

- [ ] **Step 2: 运行全量定向回归与静态验证**

Run:

```powershell
$env:TZ = 'Asia/Shanghai'
npm --workspace apps/web run test -- src/features/monitoring/model/inspectionPageBatchOperation.test.ts src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx --testTimeout=20000
npm --workspace apps/web run type-check
npm run build
Push-Location apps/manager-server
go test -count=1 ./internal/service/authfilesbanneddelete ./internal/http/controller/tokenrecovery ./internal/service/tokenrecovery
Pop-Location
```

Expected: 全部通过。此功能未改 Go 代码，但 Manager 回归证明现有 100 项、封禁和恢复服务边界未被错误假设。

- [ ] **Step 3: 运行 Linux Manager 全套件并检查提交差异**

Run:

```powershell
docker run --rm --platform linux/amd64 --mount "type=bind,source=$wt,target=/src" -w /src/apps/manager-server golang:1.26-alpine sh -lc 'export PATH=/usr/local/go/bin:$PATH; go test ./... -count=1'
git diff --check
git status --short
git log --oneline origin/lo/custom..HEAD
```

Expected: Linux Go suite PASS，`git diff --check` 无输出，唯一未提交改动是二改记忆文档。

- [ ] **Step 4: 提交记忆与计划执行状态**

```powershell
git add docs/LO-CPAM-CUSTOM-MEMORY.md
git commit -m "docs: retain inspection snapshot actions"
```

### Task 6: 合并到 `lo/custom` 并构建同一候选镜像

**Files:**
- Modify: none in `source`; only fast-forward the clean `lo/custom` worktree after verification
- Test: candidate version, image identity, pre-promotion CPA identity

- [ ] **Step 1: 复核隔离分支、源 WIP 和远端推送方向**

Run:

```powershell
$wt = 'C:\Users\Administrator\.config\superpowers\worktrees\cpa-manager\inspection-global-actions-20261111'
$base = 'C:\Users\Administrator\.config\superpowers\worktrees\cpa-manager\upstream-v1.13.2-merge'
git -C $wt status --short --branch
git -C 'C:\kyon\docker\cpa-manager\source' status --short --branch
git -C $base status --short --branch
git -C $base remote get-url --push origin
git -C $base remote get-url --push upstream
```

Expected: feature branch clean；`source` 只读且不被切换/重置；base `lo/custom` clean；`origin` 是 LO 私有仓库且 `upstream` push URL 为 `DISABLED`。

- [ ] **Step 2: 仅快进已验证提交并推送自己的 `lo/custom`**

Run:

```powershell
git -C $base merge --ff-only work/inspection-global-actions-20261111
git -C $base push origin lo/custom
$localSha = (git -C $base rev-parse HEAD).Trim()
$remoteSha = (git -C $base ls-remote origin refs/heads/lo/custom).Split("`t")[0]
if ($localSha -ne $remoteSha) { throw "origin/lo/custom mismatch: $localSha != $remoteSha" }
```

Expected: fast-forward only；不向 `upstream` 推送；远端 SHA 相同。

- [ ] **Step 3: 从已验证隔离 worktree 构建 Linux/amd64 CPAM 候选**

Run:

```powershell
Set-Location $wt
$shortSha = (git rev-parse --short HEAD).Trim()
$version = "1.14.11-lo-custom-inspection-snapshot-actions-20261111-$shortSha"
$tag = "cpa-manager-plus:lo-custom-v$version"
docker build --pull --platform linux/amd64 --build-arg "VERSION=$version" --build-arg "SOURCE_COMMIT=$shortSha" -t $tag -f Dockerfile.manager-server .
$imageId = (docker image inspect -f '{{.Id}}' $tag).Trim()
$reportedVersion = (& docker run --rm --entrypoint /usr/local/bin/cpa-manager-plus $tag --version | Out-String).Trim()
if ($reportedVersion -notmatch [regex]::Escape($shortSha)) { throw "candidate version lacks source commit: $reportedVersion" }
"TAG=$tag"
"IMAGE_ID=$imageId"
"VERSION=$reportedVersion"
```

Expected: image build、image ID 和内嵌 source commit 可复核；不从 `source` 目录构建。

### Task 7: 直接双端部署与验收（用户已授权）

**Files:**
- Modify: local CPAM compose image line through protected script only; server `/home/cpam/app/cpa-manager-plus` only
- Preserve: `cli-proxy-api`、`cpa.service`、`/home/cpa`、服务器 `/home/cpam/config/cpam.env`、`/home/cpam/secrets/*`、`/home/cpam/data/*`、OpenResty/Certbot

- [ ] **Step 1: 记录本地 CPA Core 身份并使用受保护脚本仅提升 CPAM**

Run:

```powershell
$root = 'C:\kyon\docker\cpa-manager'
$cpaBefore = (docker inspect -f '{{.Id}}|{{.State.StartedAt}}|{{.Config.Image}}|{{.Image}}' cli-proxy-api).Trim()
$cpamBefore = (docker inspect -f '{{.Id}}|{{.State.StartedAt}}|{{.Config.Image}}|{{.Image}}' cpa-manager-plus).Trim()
function global:Read-Host {
  param([string]$Prompt)
  Write-Host "$Prompt [authorized by LO]"
  'PROMOTE_CPAM'
}
& "$root\deploy-cpa-manager-key-policy.ps1" -Promote -CandidateImage $tag
$cpaAfter = (docker inspect -f '{{.Id}}|{{.State.StartedAt}}|{{.Config.Image}}|{{.Image}}' cli-proxy-api).Trim()
if ($cpaBefore -ne $cpaAfter) { throw 'CPA Core identity changed during CPAM-only promotion.' }
```

Expected: 仅 `cpa-manager-plus` 被 force-recreate；部署脚本创建本地备份；CPA Core identity 完全相同；任何本地 health/port guard 失败由脚本自动回滚 CPAM，不重启 CPA。

- [ ] **Step 2: 从同一候选镜像提取二进制并校验 SHA-256**

Run:

```powershell
$artifactDir = Join-Path $root "_deploy_artifacts\inspection-snapshot-actions-20261111-$shortSha"
New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null
$artifact = Join-Path $artifactDir 'cpa-manager-plus'
$tmpContainer = "cpam-artifact-$shortSha"
docker create --name $tmpContainer $tag | Out-Null
try { docker cp "${tmpContainer}:/usr/local/bin/cpa-manager-plus" $artifact } finally { docker rm -f $tmpContainer | Out-Null }
$artifactSha = (Get-FileHash -Algorithm SHA256 -LiteralPath $artifact).Hash.ToLowerInvariant()
"ARTIFACT=$artifact"
"SHA256=$artifactSha"
```

Expected: 只导出候选二进制；不会传输 SQLite、`data.key`、secrets 或 Docker volume。

- [ ] **Step 3: 通过 `LACPA-VPS` 原子替换服务器 CPAM 二进制，仅重启 `cpam.service`**

先通过 `LACPA-VPS` 记录：

```bash
set -euo pipefail
systemctl is-active cpa.service
systemctl is-active cpam.service
systemctl show --property=MainPID --value cpa.service
sha256sum /home/cpam/data/data.key
stat -c '%U:%G %a %n' /home/cpam/app/cpa-manager-plus /home/cpam/data/usage.sqlite /home/cpam/data/data.key
```

将 `$artifact` 仅上传到 `/home/cpam/_incoming/cpa-manager-plus-$shortSha`，服务器核对
`sha256sum` 等于 `$artifactSha` 后执行：

```bash
set -euo pipefail
short_sha='<shortSha>'
artifact_sha='<artifactSha>'
incoming="/home/cpam/_incoming/cpa-manager-plus-$short_sha"
app='/home/cpam/app/cpa-manager-plus'
release="/home/cpam/_releases/cpam-$(date -u +%Y%m%dT%H%M%SZ)-$short_sha"
cpa_pid_before="$(systemctl show --property=MainPID --value cpa.service)"
test "$(sha256sum "$incoming" | awk '{print $1}')" = "$artifact_sha"
install -d -m 0755 "$release"
cp -p "$app" "$release/cpa-manager-plus"
owner="$(stat -c '%u' "$app")"
group="$(stat -c '%g' "$app")"
mode="$(stat -c '%a' "$app")"
install -o "$owner" -g "$group" -m "$mode" "$incoming" "$app.new"
mv -f "$app.new" "$app"
printf 'version=%s\ncommit=%s\nsha256=%s\ndeployed_at=%s\n' \
  '<candidateVersion>' "$short_sha" "$artifact_sha" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  > /home/cpam/DEPLOYMENT-MANIFEST.new
install -o "$owner" -g "$group" -m 0644 /home/cpam/DEPLOYMENT-MANIFEST.new /home/cpam/DEPLOYMENT-MANIFEST
rm -f /home/cpam/DEPLOYMENT-MANIFEST.new
systemctl restart cpam.service
systemctl is-active cpam.service
curl -fsS http://127.0.0.1:18317/health
cpa_pid_after="$(systemctl show --property=MainPID --value cpa.service)"
test "$cpa_pid_before" = "$cpa_pid_after"
systemctl is-active cpa.service
systemctl is-active openresty-cpa.service
curl -fsS https://rcpam.kyon666.top/health
```

若新的 `cpam.service` 健康失败：仅从 `$release/cpa-manager-plus` 原子恢复 `$app`，再只重启
`cpam.service`；严禁重启 `cpa.service`。任何 data/secrets/config/OpenResty 改动都属于失败。

- [ ] **Step 4: 双端部署后验证与功能烟测**

Run locally:

```powershell
docker inspect -f '{{.Id}}|{{.State.Status}}|{{.State.Health.Status}}|{{.Config.Image}}|{{.Image}}' cpa-manager-plus
Invoke-WebRequest -UseBasicParsing -Uri http://127.0.0.1:18317/health -TimeoutSec 10
docker inspect -f '{{.Id}}|{{.State.StartedAt}}|{{.Config.Image}}|{{.Image}}' cli-proxy-api
docker logs --tail 80 cpa-manager-plus
```

通过 `LACPA-VPS` 再确认：`cpam.service=active`、本地/公网 `/health=200`、`cpa.service` PID 与部署前一致、
`data.key` SHA-256 不变、`usage.sqlite` 仍存在。浏览器打开本地巡检“重新登录”筛选，确认：

- `更新全部需重新登录凭证 (N)` 与 `删除所有已确认封禁账号` 出现；
- 当前页按钮仍存在；
- 不点击新按钮时 Network 不出现全量 `token-recovery/query`；
- 不显示旧的“删除需重新登录账号 (N)”本地危险入口；
- 不点击 destructive confirm，不对生产凭证执行实际删除。

- [ ] **Step 5: 发布记录**

报告 commit、origin `lo/custom` SHA、候选 tag/image ID/version、local backup、server backup、server binary SHA、
本地/服务器 health、CPA Core 容器 ID 与 `cpa.service` PID 未变，并明确本次未触碰计费、额度、派生库和 CPA Core。
