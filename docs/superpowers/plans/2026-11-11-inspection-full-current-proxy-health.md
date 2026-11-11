# 全量当前代理出口健康复检 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** 在 Codex 巡检完成后复检全部当前 `proxy_url`，让失效旧代理即使账户探测 HTTP 200 也进入既有“代理异常”筛选，同时保持零计费和无原始 URL 泄露。

**Architecture:** 浏览器从已有当前代理投影中严格解析巡检结果，以规范化 SHA-256 指纹合并重复代理，按最多 500 个代表定位字段请求 Manager。Manager 每批获取一次完整投影、内存严格匹配并合并代理 URL，以 40 路 worker 检查后把无 URL 的 outcome 扇出；浏览器把安全证据扇回所有同组行并本地持久化。

**Tech Stack:** Go 1.26 Manager 服务、React/TypeScript/Vitest、既有 `usageService`、CPAM auth-files 投影接口。

---

## 文件结构

| 文件 | 职责 |
| --- | --- |
| `apps/manager-server/internal/service/proxyreplacement/service.go` | 单次 inventory 快照、严格定位、URL 去重、40 路 credential exit-IP worker。|
| `apps/manager-server/internal/service/proxyreplacement/service_test.go` | 一次 inventory 读取、去重扇出、严格匹配、40 路并发的服务回归。|
| `apps/web/src/features/monitoring/codexInspection.ts` | 安全的复检证据字段类型。|
| `apps/web/src/features/monitoring/model/proxyHealthVerification.ts` | 浏览器分组、证据有效性、结果扇出；不暴露 raw URL 的边界。|
| `apps/web/src/features/monitoring/model/proxyHealthVerification.test.ts` | 分组/失效/扇出纯函数回归。|
| `apps/web/src/features/monitoring/model/codexInspectionStorage.ts` | 仅保存和水化安全复检字段。|
| `apps/web/src/features/monitoring/codexInspection.test.ts` | 存储不包含 raw URL 的回归。|
| `apps/web/src/features/monitoring/CodexInspectionPage.tsx` | 删除失败候选门槛，触发全量当前代理复检。|
| `apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx` | HTTP 200/禁用凭证参与、重复代理只发一个 locator 的集成回归。|
| `docs/LO-CPAM-CUSTOM-MEMORY.md` | 二改保留约束。|

### Task 1: Manager 批次 inventory 与共享代理扇出

**Files:**
- Modify: `apps/manager-server/internal/service/proxyreplacement/service_test.go`
- Modify: `apps/manager-server/internal/service/proxyreplacement/service.go`

- [x] **Step 1: 写失败测试：同一当前代理只查一次且 inventory 只抓一次**

在 `service_test.go` 构造支持 `FetchWithProxyURLs` 的 fake auth-files：两个严格不同的
`ProxyReplacementTarget` 对应同一个当前 proxy；fake exit checker 记录 URL 调用次数并
返回 `ProxyExitIPOutcomeProxyFailed`。期望：`fetchCalls == 1`、`checkerCalls == 1`，结果
长度为 2、两个 `ClientKey` 都是 `proxy_failed`。测试不得断言或打印 raw URL。

```go
batch, err := service.VerifyCredentialExitIPs(context.Background(), []model.ProxyReplacementTarget{left, right})
if err != nil { t.Fatalf("VerifyCredentialExitIPs() error = %v", err) }
if got := authFiles.fetchWithProxyURLCalls; got != 1 { t.Fatalf("inventory calls = %d, want 1", got) }
if got := checker.callCount(); got != 1 { t.Fatalf("exit checks = %d, want 1", got) }
for _, result := range batch.Results {
    if result.Outcome != model.ProxyExitIPOutcomeProxyFailed { t.Fatalf("outcome = %q", result.Outcome) }
}
```

- [x] **Step 2: 运行 RED**

Run:
```powershell
cd apps/manager-server; go test ./internal/service/proxyreplacement -run 'TestVerifyCredentialExitIPs.*(SingleInventory|SharedProxy)' -count=1
```
Expected: FAIL，因为当前实现对每个 target 调用 `ResolveProxyURLMutationTarget`，而不是一次 projection + grouped check。

- [x] **Step 3: 实现最小单快照解析与分组**

在 `VerifyCredentialExitIPs` 的 setup 解析之后调用一次 `s.fetchInventory(ctx, setup)`。
新增一个只在内存工作的严格解析函数：按 `RuntimeID`/物理文件名/provider/auth index
缩小候选并调用既有 `cpaauthfiles.VerifyResolvedIdentity`；零个、多个或身份不符返回
`inconclusive` 结果。将有效 `ProxyURL` 用既有规范化规则分组。worker job 是一组 URL，
完成后复制 outcome/reason 到该组的所有 `ClientKey`。不改变 handler、上限或返回 schema。

- [x] **Step 4: 运行 GREEN**

Run:
```powershell
cd apps/manager-server; go test ./internal/service/proxyreplacement -run 'TestVerifyCredentialExitIPs.*(SingleInventory|SharedProxy)' -count=1
```
Expected: PASS。

- [x] **Step 5: 提交该可独立验证的后端单元**

```powershell
git add apps/manager-server/internal/service/proxyreplacement/service.go apps/manager-server/internal/service/proxyreplacement/service_test.go
git commit -m "feat: group credential proxy exit checks"
```

### Task 2: Manager 并发与严格身份回归

**Files:**
- Modify: `apps/manager-server/internal/service/proxyreplacement/service_test.go`
- Modify: `apps/manager-server/internal/service/proxyreplacement/service.go`

- [x] **Step 1: 写失败测试：credential 40 路、代理池不变、歧义不探测**

使用 41 个不同的唯一 proxy 及会阻塞到 gate 的 fake checker。观察在释放 gate 前至少 40
个已进入 checker；另写一个含两个相同 identity 候选的 inventory，期望该 target 为
`inconclusive` 且 checker 调用数为 0。已有 pool checker 测试仍应验证其原 8 路语义。

- [x] **Step 2: 运行 RED**

Run:
```powershell
cd apps/manager-server; go test ./internal/service/proxyreplacement -run 'TestVerifyCredentialExitIPs.*(Forty|Ambiguous)' -count=1
```
Expected: FAIL，现有共享 `proxyExitIPCheckConcurrency` 是 8 且 per-target resolution 可绕开快照歧义检测。

- [x] **Step 3: 实现独立 credential worker 常量**

将通用常量拆为 `proxyPoolExitIPCheckConcurrency = 8` 与
`credentialExitIPCheckConcurrency = 40`。仅 credential grouped jobs 使用 40；pool 路径
保持 8。严格解析失败返回只含 client key/outcome/reason 的 `inconclusive`，不得调用 checker。

- [x] **Step 4: 运行 GREEN 及包回归**

Run:
```powershell
cd apps/manager-server; go test ./internal/service/proxyreplacement -count=1
```
Expected: PASS。

- [x] **Step 5: 提交**

```powershell
git add apps/manager-server/internal/service/proxyreplacement/service.go apps/manager-server/internal/service/proxyreplacement/service_test.go
git commit -m "perf: raise grouped credential proxy checks to forty"
```

### Task 3: 浏览器复检证据纯模型

**Files:**
- Create: `apps/web/src/features/monitoring/model/proxyHealthVerification.ts`
- Create: `apps/web/src/features/monitoring/model/proxyHealthVerification.test.ts`
- Modify: `apps/web/src/features/monitoring/codexInspection.ts`

- [x] **Step 1: 写失败测试：HTTP 200/禁用行和重复代理被安全合并**

构造两个具有当前代理投影的结果：一个 `statusCode: 200, disabled: true`，另一个任意
状态；它们共享同一 current proxy。期望 `collectCurrentProxyHealthGroups` 返回一组、只含
一个代表 locator、成员 key 有两个。把 `proxy_failed` 应用回组后两个结果均为
`proxySuspect: true` 且有相同 fingerprint；把 `healthy` 应用后两个都是 false。再断言
fingerprint 不同时 `hasValidProxyHealthVerification` 为 false。

- [x] **Step 2: 运行 RED**

Run:
```powershell
npm --workspace apps/web run test -- src/features/monitoring/model/proxyHealthVerification.test.ts
```
Expected: FAIL，因为 module 和字段尚不存在。

- [x] **Step 3: 最小实现纯函数与类型**

在结果类型加入：

```ts
proxyVerificationOutcome?: 'healthy' | 'proxy_failed' | 'inconclusive' | 'not_saved';
proxyVerificationReason?: string;
proxyVerificationFingerprint?: string;
proxyVerificationCheckedAtMs?: number;
```

模型使用 `proxyReplacementFingerprint`，只返回 locator/fingerprint/member key；将 Manager
result 通过 `clientKey` 映射到组后扇出。未知/缺失 outcome 规范为 `inconclusive`；仅
`proxy_failed` 设置 `proxySuspect`。不要把 URL 放进公开结果或持久化对象。

- [x] **Step 4: 运行 GREEN**

Run:
```powershell
npm --workspace apps/web run test -- src/features/monitoring/model/proxyHealthVerification.test.ts
```
Expected: PASS。

- [x] **Step 5: 提交**

```powershell
git add apps/web/src/features/monitoring/codexInspection.ts apps/web/src/features/monitoring/model/proxyHealthVerification.ts apps/web/src/features/monitoring/model/proxyHealthVerification.test.ts
git commit -m "feat: model current proxy health evidence"
```

### Task 4: 安全持久化与旧结果失效

**Files:**
- Modify: `apps/web/src/features/monitoring/model/codexInspectionStorage.ts`
- Modify: `apps/web/src/features/monitoring/codexInspection.test.ts`

- [x] **Step 1: 写失败测试：安全字段往返且 raw proxy 不入存储**

保存包含 outcome、reason、fingerprint、timestamp 的结果，读取后断言字段存在。对序列化
文本断言不包含测试 URL。加载旧式 `proxySuspect: true` 但无 fingerprint 的记录，断言
其没有有效可复用的 exit-IP 证据。

- [x] **Step 2: 运行 RED**

Run:
```powershell
npm --workspace apps/web run test -- src/features/monitoring/codexInspection.test.ts
```
Expected: FAIL，因为 storage 尚未读写新增字段。

- [x] **Step 3: 最小 storage 修改**

将新增字段限定为上述 enum、净化 reason、64 位 hex fingerprint、非负毫秒值。旧 schema
不进行 raw URL migration；缺 fingerprint 的旧 exit-IP 标记只作为待复检记录。

- [x] **Step 4: 运行 GREEN**

Run:
```powershell
npm --workspace apps/web run test -- src/features/monitoring/codexInspection.test.ts
```
Expected: PASS。

- [x] **Step 5: 提交**

```powershell
git add apps/web/src/features/monitoring/model/codexInspectionStorage.ts apps/web/src/features/monitoring/codexInspection.test.ts
git commit -m "feat: persist safe proxy health evidence"
```

### Task 5: 页面接线与全量复检回归

**Files:**
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx`
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.tsx`

- [x] **Step 1: 写失败生命周期测试**

让已有页面 harness 返回一个禁用且 probe HTTP 200 的 current-proxy credential，以及另一
个共享当前代理的 credential。mock `verifyCredentialExitIPs` 并断言浏览器只提交一个
strict target（请求体无 `proxy_url`），且返回 `proxy_failed` 后两个行均显示/可筛选为
proxy abnormal。旧的“只在网络失败候选时调用”断言改为“不依赖 probe status”。

- [x] **Step 2: 运行 RED**

Run:
```powershell
npm --workspace apps/web run test -- src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx
```
Expected: FAIL，因为现有效果依赖 `isLocalProxyExitIPCandidate`。

- [x] **Step 3: 接入纯模型**

页面在巡检最终结果和 current auth projection 都可用时，收集无有效同指纹证据的全部组，
按 500 顺序调用现有 `usageService.verifyCredentialExitIPs`，每批返回立即扇出并更新
state。保留 cancellation guard 和一次运行去重 ref，但 key 改为 fingerprint/运行边界，
不可让旧代理证据污染新 current proxy。展示过滤时只信任指纹仍匹配的证据。移除该效果对
`isLocalProxyExitIPCandidate` 的依赖；不要改变原账户探测分类。

- [x] **Step 4: 运行 GREEN 和 monitoring 定向测试**

Run:
```powershell
npm --workspace apps/web run test -- src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx src/features/monitoring/model/proxyHealthVerification.test.ts src/features/monitoring/codexInspection.test.ts
```
Expected: PASS。

- [x] **Step 5: 提交**

```powershell
git add apps/web/src/features/monitoring/CodexInspectionPage.tsx apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx
git commit -m "feat: inspect every current credential proxy"
```

### Task 6: 最终验证、记忆核对与交付

**Files:**
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`
- Create: `docs/superpowers/specs/2026-11-11-inspection-full-current-proxy-health-design.md`
- Create: `docs/superpowers/plans/2026-11-11-inspection-full-current-proxy-health.md`

- [x] **Step 1: 运行后端与前端定向验证**

```powershell
cd apps/manager-server; go test ./internal/service/proxyreplacement ./internal/http/controller/proxyreplacement -count=1
cd ../..; npm --workspace apps/web run test -- src/features/monitoring
npm --workspace apps/web run type-check
```
Expected: 全部 PASS。

- [x] **Step 2: 运行构建验证**

```powershell
npm --workspace apps/web run build
```
Expected: production build 成功。

- [x] **Step 3: 复查边界**

```powershell
git diff --check
git diff --name-only HEAD~1..HEAD
git status --short
```
Expected: 无空白错误；只含 CPAM web/manager-server/docs；不会出现 CPA Core 或 `cli-proxy-api` 文件。

- [x] **Step 4: 最终提交（如前序任务未逐项提交则合并一次）**

```powershell
git add docs/LO-CPAM-CUSTOM-MEMORY.md docs/superpowers/specs/2026-11-11-inspection-full-current-proxy-health-design.md docs/superpowers/plans/2026-11-11-inspection-full-current-proxy-health.md
git commit -m "docs: record full current proxy health inspection"
```

- [x] **Step 5: 交付但不部署**

报告 commit、定向测试、type-check/build 结果和“未部署”。只有用户明确说“部署”时，才按 CPAM 双端部署约定操作。

## 执行记录（2026-11-11）

- [x] 后端：单批 `FetchWithProxyURLs` 快照、严格身份校验、相同当前代理去重扇出；credential
  exit-IP 检查并发为 40，保存代理池检查继续为 8。
- [x] 前端：所有有当前代理的巡检行（含禁用和 HTTP 200）进入独立复检；每个当前代理指纹
  只提交一个严格 locator，最多 500 组/请求。
- [x] 持久化：只保存 outcome、净化 reason、fingerprint、timestamp；旧的无 fingerprint
  标记会在当前代理投影到达后重检，不再作为异常展示。
- [x] TDD：新增回归先失败后实现；涵盖共享代理、禁用 HTTP 200、40 路上限、歧义拒绝、
  fingerprint 失效、raw URL 不持久化。
- [x] 验证：`go test ./internal/service/proxyreplacement ./internal/http/controller/proxyreplacement -count=1`
  通过；`npm --workspace apps/web run test -- src/features/monitoring` 通过（58 files / 715 tests）；
  `npm --workspace apps/web run build` 以 `EXIT:0` 完成。
- [x] 实现提交：`b2484b5e`、`137388fc`、`c38e15f3`。未部署；部署需要用户单独确认。
