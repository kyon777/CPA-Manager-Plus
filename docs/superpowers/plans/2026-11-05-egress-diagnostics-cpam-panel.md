# 出口 IP → 账号绑定监控 CPAM 插件页 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在既有“Codex 全局限流”插件页内安全显示 CPA 的出口 IP→账号绑定与最近诊断事件。

**Architecture:** CPA 插件继续是诊断数据唯一来源。嵌入式插件页通过固定 `diagnostics` postMessage 请求 CPAM；CPAM 只在已验证的 iframe source/origin 上调用一个硬编码的管理路径，并将经过字段、长度和数量校验的只读投影发回 iframe。监控轮询在 iframe 的监控标签可见时每 5 秒运行，其他时间无后台请求。

**Tech Stack:** Go 1.26 CPA plugin、TypeScript/React 19 CPAM、Vitest、Linux/amd64 Go shared-object build、Docker Compose release staging。

---

## 文件结构

| 位置 | 责任 |
|---|---|
| `C:/Users/Administrator/.config/superpowers/worktrees/per-auth-rate-limit/egress-diagnostics-panel-20261105/management_ui.go` | 嵌入式插件页：配置/监控双标签、固定诊断桥接、可见时轮询和表格渲染。 |
| `.../management_ui_ttl_test.go` | 断言 HTML 中存在固定 diagnostics 协议、5 秒可见轮询与无通用 URL 转发。 |
| `C:/Users/Administrator/.config/superpowers/worktrees/cpa-manager/egress-diagnostics-panel-20261105/apps/web/src/features/plugins/pluginRateLimitBridge.ts` | CPAM 限流桥接请求/响应类型、严格解析、诊断投影、host 处理。 |
| `.../pluginRateLimitBridge.test.ts` | 信任边界、limit、数据投影和旧 load/save 行为测试。 |
| `.../apps/web/src/services/api/plugins.ts` | 唯一的固定 CPA diagnostics API 调用。 |
| `.../apps/web/src/services/api/plugins.test.ts` | 固定路径、合法 limit、scoped request 的 API 测试。 |
| `.../apps/web/src/features/plugins/PluginResourcePage.tsx` | 为该桥接提供按当前 CPA connection scope 绑定的 `getDiagnostics` 回调。 |
| `C:/kyon/docker/cli-proxy-api/source/CPA_UPDATE_RUNBOOK.md` | 记录新插件 UI/CPAM bridge 私有契约，供今后 CPA 更新保留。 |
| `C:/kyon/docker/cpa-manager/...` deployment artifacts | 仅在构建、校验通过后生成 CPAM 的手动激活/回滚脚本；不得自动重启。 |

### Task 1: 为 CPAM 桥接定义并验证严格的 diagnostics 消息

**Files:**
- Modify: `apps/web/src/features/plugins/pluginRateLimitBridge.test.ts`
- Modify: `apps/web/src/features/plugins/pluginRateLimitBridge.ts`

- [ ] **Step 1: 写入 diagnostics 成功路径的失败测试**

在 `pluginRateLimitBridge.test.ts` 添加测试，构造受信 origin/source 的消息：

```ts
{
  protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
  pluginID: RATE_LIMIT_BRIDGE_PLUGIN_ID,
  requestID: 'egress_diag_1',
  operation: 'diagnostics',
  limit: 100,
}
```

注入 `getDiagnostics: vi.fn(async () => ({ generatedAt, bindings, events }))`，断言 callback 仅被调用一次且 reply 是：

```ts
expect(reply).toMatchObject({
  requestID: 'egress_diag_1',
  operation: 'result',
  ok: true,
  diagnostics: {
    bindings: [{ egressHost: '64.8.252.252', ownerAuthID: 'account-a', inFlight: 1 }],
    events: [{ event: 'owner_bound', egressHost: '64.8.252.252', authID: 'account-a' }],
  },
});
```

- [ ] **Step 2: 运行测试，确认因尚无 diagnostics bridge 而失败**

Run:

```powershell
npm --workspace apps/web run test -- src/features/plugins/pluginRateLimitBridge.test.ts
```

Expected: FAIL，diagnostics 消息被忽略或 `getDiagnostics` option/type 不存在；不得因为测试拼写错误失败。

- [ ] **Step 3: 写入非法输入与数据脱敏的失败测试**

新增独立测试：

1. `limit: 0`、`1025`、`1.5`、字符串均不会调用 `getDiagnostics`；
2. source 或 origin 不匹配时不会调用；
3. upstream data 中 `proxy_url`、`access_token`、`refresh_token`、`proxy_port` 不出现在 reply；
4. 事件多于请求 limit 时 reply 只保留请求的上限；
5. 不合法 host（含 `@`、`:`、`/` 或空白）、超长账号 ID、不合法事件名、无效时间戳被丢弃。

- [ ] **Step 4: 实现最小 diagnostics bridge 合同**

在 `pluginRateLimitBridge.ts` 增加以下公开、可判别类型：

```ts
export interface RateLimitBridgeDiagnosticsRequest {
  protocol: typeof RATE_LIMIT_BRIDGE_PROTOCOL;
  pluginID: typeof RATE_LIMIT_BRIDGE_PLUGIN_ID;
  requestID: string;
  operation: 'diagnostics';
  limit: number;
}

export interface RateLimitEgressDiagnostics {
  generatedAt: string;
  bindings: RateLimitEgressDiagnosticBinding[];
  events: RateLimitEgressDiagnosticEvent[];
}
```

实现规则：

- `parseRateLimitBridgeRequest` 只接受固定 operation `load`、`save`、`diagnostics`；diagnostics 可省略 limit 并归一化为 `100`，只接受安全整数 `1..1024`。
- `RateLimitBridgeHostOptions` 新增 `getDiagnostics: (limit: number) => Promise<unknown>`。
- host 对 diagnostics 直接调用该回调；不接触 save queue、`getConfig` 或 `patchConfig`。
- 添加 `normalizeRateLimitEgressDiagnostics`，只构造白名单字段并限制：最大 1024 events、最大 4096 bindings、字符串长度不超过 512（reason 128），host 只接受裸 hostname/IP，时间必须能被 `Date.parse` 解析，`inFlight` 是非负安全整数。所有未知字段都丢弃。
- `createRateLimitBridgeSuccess` 根据 request 类型生成 `settings` 或 `diagnostics`；错误 envelope 仍只含固定 `error` 字符串。

- [ ] **Step 5: 运行桥接测试，确认通过**

Run:

```powershell
npm --workspace apps/web run test -- src/features/plugins/pluginRateLimitBridge.test.ts
```

Expected: PASS，既有 22 个以上测试与新增 diagnostics 测试均通过。

- [ ] **Step 6: 提交 CPAM bridge contract**

```powershell
git add apps/web/src/features/plugins/pluginRateLimitBridge.ts apps/web/src/features/plugins/pluginRateLimitBridge.test.ts
git commit -m "feat: bridge rate limit egress diagnostics"
```

### Task 2: 添加固定 CPA diagnostics API 调用

**Files:**
- Modify: `apps/web/src/services/api/plugins.test.ts`
- Modify: `apps/web/src/services/api/plugins.ts`

- [ ] **Step 1: 写入固定路径的失败测试**

Mock `apiClient.get`，调用：

```ts
await pluginsApi.getRateLimitEgressDiagnostics(100, {
  apiBase: 'https://cpa.example.test',
  managementKey: 'management-key',
});
```

断言唯一调用为：

```ts
expect(mocks.get).toHaveBeenCalledWith(
  '/plugins/per-auth-rate-limit/egress-diagnostics',
  expect.objectContaining({ params: { limit: 100 }, cpampScopedRequest: true })
);
```

并增加 `0`、`1025`、非整数抛出本地错误且零 HTTP 调用的测试。

- [ ] **Step 2: 运行 API 测试，确认失败**

Run:

```powershell
npm --workspace apps/web run test -- src/services/api/plugins.test.ts
```

Expected: FAIL，`getRateLimitEgressDiagnostics` 尚不存在。

- [ ] **Step 3: 实现唯一硬编码 API 方法**

在 `pluginsApi` 增加：

```ts
async getRateLimitEgressDiagnostics(
  limit: number,
  requestScope?: ApiClientRequestScope
): Promise<unknown> {
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 1024) {
    throw new Error('invalid diagnostics limit');
  }
  return apiClient.get('/plugins/per-auth-rate-limit/egress-diagnostics', {
    ...(requestScope ? createScopedApiRequestConfig(requestScope) : {}),
    params: { limit },
  });
}
```

不得接收插件 ID、路径、URL、方法或任意 query 对象作为调用者参数。

- [ ] **Step 4: 运行 API 测试，确认通过**

Run:

```powershell
npm --workspace apps/web run test -- src/services/api/plugins.test.ts
```

Expected: PASS。

- [ ] **Step 5: 提交固定 API 方法**

```powershell
git add apps/web/src/services/api/plugins.ts apps/web/src/services/api/plugins.test.ts
git commit -m "feat: add fixed rate limit diagnostics API"
```

### Task 3: 将 diagnostics 能力接入受控 CPAM iframe host

**Files:**
- Modify: `apps/web/src/features/plugins/PluginResourcePage.tsx`
- Test: `apps/web/src/features/plugins/pluginRateLimitBridge.test.ts`

- [ ] **Step 1: 补充 scoped connection 的失败桥接测试**

在 bridge host 测试中断言 diagnostics callback 接收固定 `100`，并确保诊断请求不调用 `getConfig` 或 `patchConfig`。

- [ ] **Step 2: 运行失败测试**

Run:

```powershell
npm --workspace apps/web run test -- src/features/plugins/pluginRateLimitBridge.test.ts
```

Expected: FAIL，若 Step 1 已落实则显示 callback/queue 行为不符合测试；不得跳过此步骤。

- [ ] **Step 3: 接入页面 effect**

在 `PluginResourcePage.tsx` 现有 `createRateLimitBridgeHost` options 中添加：

```ts
getDiagnostics: (limit) =>
  pluginsApi.getRateLimitEgressDiagnostics(limit, rateLimitBridgeRequestScope),
```

保留既有 `iframeRef.current?.contentWindow === source`、精确 frame origin、`replyToTrustedPluginFrame`、connection scope memo 和 effect dispose。不得把 management key 写入 iframe URL、window 全局变量或 postMessage payload。

- [ ] **Step 4: 运行 bridge 测试及 TypeScript 检查**

Run:

```powershell
npm --workspace apps/web run test -- src/features/plugins/pluginRateLimitBridge.test.ts
npm --workspace apps/web run type-check
```

Expected: PASS。

- [ ] **Step 5: 提交 iframe host 接线**

```powershell
git add apps/web/src/features/plugins/PluginResourcePage.tsx apps/web/src/features/plugins/pluginRateLimitBridge.test.ts
git commit -m "feat: connect egress diagnostics plugin bridge"
```

### Task 4: 用 TDD 增加插件页的监控标签和可见轮询

**Files:**
- Modify: `C:/Users/Administrator/.config/superpowers/worktrees/per-auth-rate-limit/egress-diagnostics-panel-20261105/management_ui_ttl_test.go`
- Modify: `C:/Users/Administrator/.config/superpowers/worktrees/per-auth-rate-limit/egress-diagnostics-panel-20261105/management_ui.go`

- [ ] **Step 1: 写入页面契约的失败 Go 测试**

添加 `TestSettingsPageIncludesBoundedEgressDiagnosticsMonitor`，断言 `settingsPageHTML` 含：

```go
`id="settings-tab"`
`id="diagnostics-tab"`
`requestBridge('diagnostics'`
`window.setInterval(refreshDiagnostics, 5000)`
`document.visibilityState === 'visible'`
`egress_host`
```

并断言页面不包含通用 `fetch(` 或动态 endpoint 字段（允许既有固定 `configEndpoint`），以及不含 `proxy_url`、`access_token`、`refresh_token`、`managementKey`。

- [ ] **Step 2: 用 CPA core replace 环境运行测试，确认失败**

Run:

```powershell
& .\build-plugin.ps1 -CoreSourceRoot 'C:\kyon\docker\cli-proxy-api\source' -OutputRoot 'C:\Users\Administrator\.config\superpowers\worktrees\per-auth-rate-limit\_artifacts-red-egress-diagnostics-panel-20261105'
```

Expected: FAIL，新的 HTML identifiers/diagnostics operation 尚不存在。

- [ ] **Step 3: 在 `settingsPageHTML` 实现双标签和诊断渲染**

实现应仅使用固定 bridge operation：

```js
requestBridge('diagnostics', null, 100)
```

并包含：

- `限流设置` 与 `出口绑定监控` 两个无刷新标签；默认显示设置；
- 当前绑定表：出口 IP、账号、in-flight、保持原因、截止/剩余时间、更新时间、延迟释放；
- 最近事件表：时间、事件、出口 IP、账号、原因、保持信息；
- `立即刷新`、最后刷新时间、加载/错误/空状态；
- `refreshInFlight` 互斥锁；切入监控后立即刷新；监控标签活动且 `document.visibilityState === 'visible'` 时设置唯一 5000ms interval；切出标签、visibility hidden、beforeunload 时清 interval；
- response handler 根据 `message.diagnostics` resolve diagnostics promise，而既有 `message.settings` 继续只服务 load/save。

禁止 `fetch`、禁止任意 endpoint、禁止 proxy URL/credential 渲染，保留现有主题 bridge 与所有设置保存逻辑。

- [ ] **Step 4: 运行插件测试并构建临时 Linux 工件**

Run:

```powershell
& .\build-plugin.ps1 -CoreSourceRoot 'C:\kyon\docker\cli-proxy-api\source' -OutputRoot 'C:\Users\Administrator\.config\superpowers\worktrees\per-auth-rate-limit\_artifacts-green-egress-diagnostics-panel-20261105'
```

Expected: Go tests PASS，输出 Linux/amd64 `.so`。确认 `file`/ELF header 与 SHA-256。

- [ ] **Step 5: 提交插件 UI**

```powershell
git add management_ui.go management_ui_ttl_test.go
git commit -m "feat: add egress diagnostics monitor tab"
```

### Task 5: 更新私有更新记忆、构建和发布手动激活包

**Files:**
- Modify: `C:/kyon/docker/cli-proxy-api/source/CPA_UPDATE_RUNBOOK.md`
- Modify/Create: CPA release staging scripts under `C:/kyon/docker/cli-proxy-api/source/`
- Modify/Create: CPAM deploy artifacts under `C:/kyon/docker/cpa-manager/`

- [ ] **Step 1: 写入 CPA 更新记忆**

在 `CPA_UPDATE_RUNBOOK.md` 添加必须保留项：`per-auth-rate-limit` 页面包含出口绑定监控标签；CPAM bridge 仅允许 `diagnostics` 固定操作，使用 `/v0/management/plugins/per-auth-rate-limit/egress-diagnostics?limit=100`；不能在更新时删除 plugin UI 或改成通用代理；两端都必须同批验证。

- [ ] **Step 2: 构建命名新版本的 Linux 插件工件**

将 `build-plugin.ps1` 工件名和 plugin metadata version 同步提升为新 patch 版本，构建到隔离 staging 输出。确认新 `.so` 与当前 core SDK 兼容，记录 SHA-256，并保留旧工件以供回滚。

- [ ] **Step 3: 生成 CPA 手动部署包**

从当前已激活 private release 脚本复制并最小化升级，替换仅：新 plugin artifact、plugin source commit、hash、version、release ID、release manifest。必须：

- 构建/校验前不停止 `cli-proxy-api`；
- 本地 Docker 和 `/home/cpa` Linux native bundle 均有原子备份/回滚；
- 不触碰 `/home/cpa/auths`、CPAM、OpenResty、Caddy 或持久策略状态；
- 默认仅生成用户手动运行的 `.bat`/activation script，不自动重启任何 CPA。

- [ ] **Step 4: 生成 CPAM 手动部署包**

以 `lo/custom` 为 base 构建 CPAM Web/Manager Server image，写入新的私有 image tag、SHA/commit 元数据、备份和回滚操作。生成 `.bat`/`.ps1`，默认只在用户手动运行时重建/重启 `cpa-manager-plus`；不得触碰 `cli-proxy-api`。

- [ ] **Step 5: 运行发布前完整验证**

Run:

```powershell
# CPAM
npm --workspace apps/web run test -- src/features/plugins/pluginRateLimitBridge.test.ts src/services/api/plugins.test.ts
npm --workspace apps/web run type-check
npm --workspace apps/web run build

# CPA plugin (with local patched core SDK)
& 'C:\Users\Administrator\.config\superpowers\worktrees\per-auth-rate-limit\egress-diagnostics-panel-20261105\build-plugin.ps1' -CoreSourceRoot 'C:\kyon\docker\cli-proxy-api\source' -OutputRoot '<isolated-staging-output>'

# release guards
Get-FileHash '<new-plugin-artifact>' -Algorithm SHA256
# run generated scripts in verify-only/stage-only mode where supported
```

Expected: all test/build commands exit 0, staged hashes match manifests, current live CPA/CPAM remain running and untouched.

- [ ] **Step 6: 提交、仅推送私有 origin、交付手动激活命令**

```powershell
git status --short
git diff --check
git push origin feature/egress-diagnostics-panel-20261105
```

仅推送 private `origin`；绝不向 upstream push。报告两个 release 包路径、SHA-256、手动激活脚本和回滚路径，不声称已部署，除非用户实际执行并提供成功验证。