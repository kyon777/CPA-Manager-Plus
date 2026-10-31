# LO 的 CPAM 二改记忆

> 这是 CPAM 二次修改的**规范清单**。执行“更新 CPAM”前必须先阅读本文件；上游代码只能合并进来，不能覆盖下面列出的行为。

更新时间：2026-10-13  
项目：`seakee/CPA-Manager-Plus` 的 LO 私有分支  
个人仓库：`https://github.com/kyon777/CPA-Manager-Plus`  
工作分支：`lo/custom`  
推送规则：只推送 `origin`（个人仓库），`upstream` 只 fetch，禁止 push。

## 1. CPAM 到 CPA Core 的实际连接方式

生产部署文件不在本仓库，而在：

```text
C:\kyon\docker\cpa-manager\compose.yaml
```

Manager 容器的关键环境变量：

```yaml
CPA_UPSTREAM_URL: "http://cli-proxy-api:8317"
```

`cpa-manager-plus` 与 `cli-proxy-api` 都加入外部 Docker 网络 `app-network`，因此 Manager 通过 Docker 内网 DNS 访问 CPA Core，不经过公网 Caddy。当前两者网络名均为 `app-network`。

通信不是浏览器直连 Core，而是：

```text
浏览器 / Caddy -> cpa-manager-plus:18317 -> cli-proxy-api:8317
```

其中 `8317` 同时承载两条 Manager→Core 通道：

- 普通管理/API 请求走 HTTP，并由 Manager 代理、补充保存的 Bearer Management Key。
- 用量队列收集走 CPA Core 同一地址上的 RESP/TCP；`USAGE_COLLECTOR_MODE=resp` 时 Manager 对 `cli-proxy-api:8317` 建立 RESP 连接并执行 `AUTH`，再按 `usage` 队列和 `right` 侧消费。

代码层的连接优先级要分清：

1. `CPA_UPSTREAM_URL` 是 Manager 的环境/兼容启动连接来源。
2. 已保存的 Manager CPA connection（加密 `manager_config_v1`，并保留 legacy setup 镜像）由 `ManagerConfigService.ResolveSetup` 解析，是运行时代理、巡检、批量上传、优先级 worker 使用的实际 `CPAUpstreamURL + ManagementKey`。
3. 因此不要把公网地址或管理 Key 写死到前端、源码、日志或本记忆文件；更换连接必须通过 Manager 的配置流程或受控部署环境变量完成。

同一部署还保留以下性能相关连接参数，更新时不得静默删掉：

```text
USAGE_COLLECTOR_MODE=resp
USAGE_RESP_QUEUE=usage
USAGE_RESP_POP_SIDE=right
USAGE_BATCH_SIZE=100
USAGE_POLL_INTERVAL_MS=500
USAGE_QUERY_LIMIT=50000
```

`TOKEN_ACQUISITION_BASE_URL` 可以保留；API Key 只从 Docker secret 文件读取，禁止进入仓库或浏览器。

## 2. 必须保留的二改功能

### 凭证列表与巡检显示

- 凭证列表和本地/服务端巡检结果长期显示 `备注: <note>`。
- 凭证列表常驻显示 `代理: <proxy_url>`；无代理时不伪造内容。
- 巡检结果显示 Codex `Credits`。
- 月额度为 0 时，Credits 可以让账号继续可用；但 5 小时额度耗尽不能被 Credits 覆盖，7 天额度耗尽可以由可用 Credits 覆盖。
- 400/失败响应合并最新 Header 快照时不能丢失已有 Credits，不能误判成“月额度耗尽”。
- 当前页可复制账号名，一行一个。
- 最近请求排序必须按全局最新请求排序，刷新后新请求仍应排在前面。

### 错误处理与优先级

- 自动降低优先级的基础状态是 HTTP `502`、`503`；不是所有 50x。另有两类精确限定的 HTTP `500` Codex responses 传输失败，见下方规则。
- HTTP `429` 只有同时满足响应体 `{"detail":"Rate limit exceeded"}` 才进入限流降优先级路径，普通 429 不触发。
- 降优先级下限为 0。
- 降级时排除当前账号，读取其他账号最高优先级：若最高值低于当前值，目标设为“其他最高值 - 1”；否则当前值减 1；最终 clamp 到 0，避免高优先级账号降一级后仍持续被选中。
- 优先级调整、失败原因和恢复状态必须可追踪，不能只显示笼统的“更新失败”。

### Codex 401 自动恢复与人工恢复

- 只针对“已启用且巡检判定需要重新登录”的 Codex 账号。
- 开关打开后，同一凭证滚动 24 小时最多自动尝试一次；从自动任务被 worker 实际 claim 时开始计时，成功或失败都进入 24 小时冷却。
- 冷却结束后必须有新的已确认 HTTP 401 / 重登录信号才能再次自动恢复；不按时间在后台自行重试。
- 列表在冷却期显示“24 小时内已自动更新”；冷却结束后保留“曾自动更新”历史标记和可读失败原因，方便人工核查。
- 自动失败后保留人工重试按钮；人工操作不占用、不清除、不延长自动冷却。
- Token Acquisition 在 Manager Server 内完成：按邮箱匹配目标 JSON，传递 `proxy_url` 给外部接口；只更新返回的 token 字段，保留 `note`、`priority`、`proxy_url`、headers 等原字段；若返回 `chatgpt_account_id`，允许覆盖写入；不要求 account_id 匹配。
- 自动/批量恢复状态、代理元数据和封禁原因通过服务端 runtime metadata 提供；敏感 token 不写入日志、任务状态、浏览器存储或普通 SQLite 记录。

### 封禁账号与代理筛选

- 能识别 `account_banned: 账号已被封禁或停用`，显示中文原因。
- 凭证列表支持删除当前页已封禁账号，数量必须按当前页实际状态统计。
- 凭证列表支持代理 URL 筛选：输入多行代理，自动去除尾部 `/`，只比较“已启用凭证”实际使用的代理；输出未使用代理，一行一个。
- 代理筛选列表保存到 Manager Server，使换浏览器/换电脑仍可继续使用。

### JSON 导入

- 粘贴认证 JSON 默认类型为 `CPA 认证 JSON`。
- 单个 JSON 的文件名自动使用 JSON 的 `email`，不再要求手填名称。
- 数组 JSON 自动拆成多个独立账号文件；多文件上传由 Manager Server 异步串行提交给 CPA Core，避免浏览器并发请求导致 30 秒超时。
- 批量上传全部成功时，前端必须明确显示“全部成功：已保存 N/N 个认证文件”；部分成功仍显示成功数/总数及失败文件名。
- 批量上传成功后失效凭证清单缓存；任务状态只返回文件名、数量和原因，不返回 token。

### 简体中文策略文案

- `accountPolicy.serverErrorPriorityDemotion_*` 的简体中文资源必须保持 UTF-8 正常中文，禁止 `????` 乱码回归。
- 文案必须与实际策略一致：只处理新入库的 HTTP `502`、`503`，响应体精确为 `{"detail":"Rate limit exceeded"}` 的 HTTP `429`，以及下方明确列出的两类 HTTP `500` Codex responses 传输失败；比较同 Provider 的已启用账号优先级，按 peer-rebalance 规则降级，最低为 0。

### API Key / 插件桥接

- 原生 CPA API Key 模型策略桥接必须保留。
- 插件资源中的 API Key 模型策略、限流管理 iframe 和队列控制桥接必须保留。
- 这些桥接只由 CPAM 代理到 CPA Core，不得把 CPA Core 的实现复制或改写到本项目。

### 凭证列表性能优化（2026-10-13）

提交：`05a25d6f perf: reduce credential list load pressure`  
生产镜像：`cpa-manager-plus:lo-custom-v1.14.6-20261013-05a25d6f`

- `/v0/management/auth-files` 使用按 setup 隔离的内存缓存：新鲜 20 秒、 stale-while-revalidate 最长 10 分钟、失败读取冷却 45 秒；写入成功后立即失效。
- 并发冷启动请求合并，避免每个页面组件重复读取 CPA Core。
- 全局“最近请求”排序使用轻量 account recent index；只有当前页拉取完整历史，保留当前页详细状态和全局排序语义。
- 多 JSON 粘贴上传改为单个异步批量作业；上限 100 个文件、总计 32 MB；服务端串行上传并在完成后清理内存字节。
- CPAM 的 502/503/429 优先级 worker 复用凭证清单缓存。
- 以上优化只改 CPAM，不改 `cli-proxy-api`、CPA Core 数据卷或 Core 配置。

## 3. 更新 CPAM 的固定流程

用户说“更新 CPAM”时按以下顺序执行：

1. 先读取本文件和项目根目录 `AGENTS.md`，再读取当前 `lo/custom` 的提交历史。
2. `git fetch upstream`，只查看上游变化；建立备份分支/标签，不在生产 dirty source 上 checkout 或 reset。
3. 将上游合并到隔离工作树的 `lo/custom`，冲突优先保留本文件第 2 节的行为；尤其检查 `AccountsPage`、`usageService`、`proxy/service.go`、`tokenrecovery`、`server_error_priority_demotion`、路由和配置文件。
4. 运行受影响 Go 测试、前端测试、`npm run build`、`git diff --check`；失败时不能部署。
5. 从干净工作树构建 CPAM 专用镜像；生产目录 `C:\kyon\docker\cpa-manager\source` 可能有 CPA/collector 的 dirty 修改，不能用新代码覆盖它。
6. 只修改生产 `compose.yaml` 中 CPAM 镜像 tag/构建元数据，执行：

   ```powershell
   docker compose -f C:\kyon\docker\cpa-manager\compose.yaml up -d --no-build --no-deps cpa-manager-plus
   ```

7. 部署后检查 Manager `/health`、容器健康状态、启动日志，并确认 `cli-proxy-api` 容器 ID 和 `StartedAt` 未改变。
8. 只推送个人仓库：`git push origin lo/custom`；严禁 `git push upstream`。
9. 若增加或改变二改功能，先更新本文件和持久化记忆，再提交代码；不要只依赖聊天记录。

## 4. 明确禁止事项

- 不修改、重启、重建或推送 CPA Core / `cli-proxy-api`。
- 不把管理 Key、Token Acquisition API Key、认证 JSON 或 token 写入 Git、前端、日志或本文件。
- 不在生产 dirty source 上做上游合并。
- 不用上游分支直接覆盖 `lo/custom`，不执行会丢失二改的 `reset --hard`。
- 不把 CPAM 的自定义功能提交给 `seakee/CPA-Manager-Plus` 上游。

## 2026-10-15：CPAM 凭证列表服务端分页与高并发优化

本次二改只涉及 CPAM Manager Server/Web，不修改 CPA Core、`cli-proxy-api`、计费采集器或 `usage_events` 数据结构。

- 新增 `GET /usage-service/credential-list` 服务端分页投影。前端首屏只请求当前页（默认最多 50 条），Manager 向 CPA Core 请求 `/v0/management/auth-files?page=1&page_size=50&include_proxy_url=1`，后台按页预热其余清单。
- 投影同一响应返回 `note`、`proxy_url`、`priority` 等非敏感字段；服务端清理 token、headers、secret 等敏感字段。`proxy_urls`、启用数量、provider 统计只用于凭证列表/IP 筛选，不参与计费。
- 列表快照按 CPA setup（上游 URL + 管理 Key 的非秘密 SHA-256）和 proxy 投影变体隔离，30 秒 TTL；凭证上传、删除、状态/字段修改会失效快照。首次并发请求通过 in-flight 合并，只让一个请求访问 CPA Core。
- “最近请求”排序使用现有 `usage_events` 索引读取，并按快照缓存 5 秒；读取失败时仅回退确定性排序，不影响列表或用量/计费数据。
- 前端对搜索输入做 300ms debounce，服务端负责搜索、provider/status 筛选和分页；请求过期会 abort。分页模式下只对当前页懒加载 `credential-runtime-metadata`，代理筛选优先使用 CPA 投影，投影预热期间不会退回下载全量原始 JSON。
- 账号 mutation 后只刷新当前 Manager 页；显式工作区刷新在分页模式下同样走当前页接口，避免重新下载 3000 个原始凭证。旧 Manager（404/405）保留 legacy 全量路径，其他错误不隐式触发大下载。
- `enabled_credential_count` 即使为 0 也显式返回，避免前端把“没有启用账号”误判为字段缺失。

验证边界：所有改动仅在 CPAM 隔离 worktree 构建；更新或部署时不得触碰 CPA Core / `cli-proxy-api`，不得迁移或覆盖 `usage.sqlite`、`data.key`、secrets。
### 分页列表语义与运行时元数据修复（2026-10-15）

- 分页模式仍然保留 CPA 的 `include_proxy_url=1` 直接投影；`note` 与 `proxy_url` 随当前页清单一起返回，普通凭证列表首屏/翻页不得为了代理或恢复状态再下载每个账号的原始 JSON。
- Manager 只对当前页 Codex 行从 CPAM SQLite 读取并附加**脱敏**的 `recovery_task`；任何认证 JSON 自身伪造的同名字段必须在投影前删除。浏览器以该任务状态渲染“已自动更新一次”、失败原因和当前页封禁删除按钮；仅对 `queued/running` 任务继续请求轻量 token-recovery batch 轮询。
- `credential-runtime-metadata` 保留给旧 Manager 回退/显式诊断路径，但正常已启用分页列表不得调用它。代理筛选继续优先用完整的 Manager 投影；投影预热期间必须标为未完成，不能退回全量原始 JSON 扫描，也不能把未知代理误判为未使用。
- 分页列表中的 `正常可用/需要处理/额度风险/已禁用/状态待确认` 卡片现在优先使用 Manager 生成的**完整库存全局摘要**；摘要 `warming/unavailable` 时才回退当前页估算，并显示同步提示。该投影只读 Manager 索引证据，不改变 CPA Core 调度。
- 当筛选条件为“已启用”且全局摘要为 `ready/stale` 时，绿色卡使用 Manager 返回的精确全局 enabled 总数，标签必须为“已启用”，不能称“正常可用”。摘要未完成时保留当前页值并提示同步中。
- 此层不得修改 CPA Core、`cli-proxy-api`、`usage_events`、计费汇总、额度计算、巡检调度或 502/503/精确限流 429 降优先级 worker。

### 2026-10-15：CPAM 全局凭证健康摘要

- `GET /usage-service/credential-list` 在完整库存快照完成后附带 `global_metrics`、`global_metrics_status` 与生成时间；统计独立于当前搜索、状态筛选和分页，因此当前页是旧的已启用账号时，新导入且尚无证据的账号仍会进入全局“状态待确认”。
- 摘要由 Manager Server 后台读取只读证据（最近请求、巡检、恢复/操作队列、额度冷却和 Header/Credits 快照）并按 `disabled → needs_attention → quota_risk → available → unconfirmed` 互斥归类；不读取或写回凭证 Token，不写入 usage/billing/quota 表。
- 前端仅在 `ready/stale` 时采用全局卡片；首次 `warming` 或证据暂不可用时保留当前页回退并显示“全局摘要同步中”。选择“已启用”时绿色卡仍显示全局 enabled 总数，其余卡片保持全局分类。
- 摘要按完整库存指纹缓存 15 秒，CPA 清单 mutation 通过现有 invalidator 清空；该功能仅改 CPAM，不改 CPA Core、`cli-proxy-api`、计费、巡检调度和 502/503/精确限流 429 降优先级逻辑。
## 2026-10-16: Full inventory list plus visible-page hydration

This replaces the 2026-10-15 browser server-paging main path. The Manager
`/usage-service/credential-list` implementation and its snapshot/global-summary
code may remain for other capabilities, but normal credential-list rendering
must not call it. During every future "update CPAM" merge, preserve:

- The normal list's only initialization and refresh path is
  `useAuthFilesData.loadFiles()`, which reads the complete CPA
  `/v0/management/auth-files?include_proxy_url=1` inventory. Browser memory
  performs global filtering, ordering, provider counts, health-card metrics
  and local pagination. Do not reconnect `useCredentialListPage`,
  `serverCredentialListEnabled`, or credential-list projection fallback code.
- When the status filter is `enabled`, preserve the established card contract:
  the green card label is `Enabled` and its value is the exact complete local
  enabled-inventory count, not merely the healthy subset on the visible page.
- `note` and `proxy_url` arrive in the same complete auth-files response.
  Proxy filtering reads `proxy_url` from enabled full-list rows; opening that
  modal must not scan all credential JSON through `credential-runtime-metadata`.
- Global latest-request ordering stays on `account-recent-index`. Detailed
  history, list window usage, and recovery state request only the visible page
  (normally <= 50 rows). Page/filter changes abort stale work. Successfully
  loaded history stays in the in-memory session cache; explicit refresh or a
  connection-scope change invalidates it.
- A detail drawer opened through a deep link may add its one selected row to
  the visible-page history request when that row is outside the page. This is
  an explicit on-demand read, never a return to full-inventory history loads.
  If a credential disappears and later reuses the same selection key, discard
  its cached history and invalidate/abort a matching targeted history request
  before the re-created row is loaded; otherwise stale history can attach to a
  new credential.
- Normal full-list recovery state uses the lightweight Manager SQLite
  `POST /v0/management/token-recovery/query` batch route and polls only
  pending tasks. It must not use `credential-runtime-metadata`. The route has
  a 100-target cap, so callers must chunk target arrays.
- Management read budgets are 60 seconds in `REQUEST_TIMEOUT_MS`,
  `USAGE_SERVICE_TIMEOUT_MS`, and `cpaauthfiles.DefaultTimeout`. This changes
  only CPAM management reads: never CPA scheduling, downstream business
  requests, billing, inspection, or the priority worker.
- Do not modify, restart, build, or deploy CPA Core / `cli-proxy-api`; this is
  CPAM Web and Manager Server work only.

The 2026-10-15 paging/global-summary sections above are historic design
records. This section is the current production behavior if they conflict.

## 2026-10-18: Inspection Current-Page Operation Feedback and Exact EOF 500

This customization is CPAM-only. It does not change CPA Core, `cli-proxy-api`,
the usage collector, scheduling, quota calculation, or billing data.

- In the local Codex inspection `reauth` result tab, **Update current-page
  credentials** must lock immediately at submission and remain locked while
  the Manager's existing redacted token-recovery task is `queued` or `running`.
  It must use the existing lightweight
  `POST /v0/management/token-recovery/query` polling path only; it must not
  reload raw auth JSON, invoke a quota refresh, or force CPA to probe the
  credential.
- After a manual recovery request returns, the inspection toolbar keeps a
  persistent visible summary of queued/running/succeeded/failed targets and
  the sanitized Manager error reason. A page refresh may reconstruct a lock
  from an already-pending durable task, so a duplicated recovery request is
  not submitted.
- In the same `reauth` result tab, **Delete current-page banned credentials**
  locks from the first confirmation through the final Manager response. Its
  persistent result must separately show `deleted`, `skipped`, and `failed`
  counts plus bounded safe per-file reason codes. Only Manager-confirmed
  physical filenames are removed from local inspection cards; skipped/failed
  cards intentionally remain visible for manual follow-up.
- Keep the Manager delete endpoint's existing safety behavior and bounded
  concurrency. Do not turn skipped identities into deletes merely to make the
  UI count look complete.
- Priority demotion remains narrow: HTTP `502`, HTTP `503`, and HTTP `429`
  only with exact JSON detail `Rate limit exceeded` are eligible. HTTP `500`
  is eligible only when its raw failure body contains the Codex responses
  request marker `Post "https://chatgpt.com/backend-api/codex/responses":`
  (case-insensitive) plus either `unexpected EOF` or the exact proxy transport
  marker `utls: dial upstream: proxy CONNECT returned status 407 Proxy
  Authentication Required` (case-insensitive). Generic HTTP 500, generic
  proxy 407, other endpoints, and other EOF messages must never be demoted.
  The existing peer-priority rebalance and zero floor remain unchanged.

## 2026-10-18: Proxy-replacement recheck reconciliation and 40-slot manual recovery

This customization is CPAM-only. It must not alter CPA Core, `cli-proxy-api`,
usage/billing collection, quota calculation, or CPA scheduling.

- An inspection-origin proxy replacement now persists the exact server
  inspection identity (`inspection_run_id` + `inspection_account_key`) with the
  durable task item. After a directed recheck succeeds, CPAM clears
  `proxy_suspect` only for that exact persisted result row. A failed recheck
  remains in the `proxy_abnormal` filter; historical runs and other accounts
  are not changed.
- Browser-local inspection results use `proxyRecheckSucceeded` in the saved
  last-run state. When the modal task finishes, it retrieves every task page
  (not just the visible 50 rows) and clears only successful targets from the
  local proxy-abnormal count. This is inventory/result-state reconciliation;
  it must not start another inspection or refresh quota, billing, or history.
- Proxy replacement task totals distinguish write and recheck outcomes:
  `failed_count` means `write_failed` only. `recheck_failed_count` remains
  separate, but any recheck failure still makes the task `partial`.
- The live Token Recovery service is configured with `WorkerCount: 40` for
  the manual current-page credential update action. All workers can claim
  manual tasks; only one may claim automatic work when manual work is absent,
  preserving bounded background behavior. The per-file mutation coordinator
  remains mandatory, so two recovery tasks for the same physical auth file
  never write concurrently. This only controls Token Acquisition/update jobs;
  it does not call CPA quota endpoints or change billing.

## 2026-10-18: Proxy-replacement pool-state recovery after directed recheck

This customization is CPAM-only. It must not alter CPA Core, `cli-proxy-api`,
credential JSON except during an already-confirmed proxy-replacement task,
usage/billing collection, quota calculation, or CPA scheduling.

- `proxy_pool_state` is a Manager-owned eligibility quarantine keyed only by
  normalized SHA-256 proxy fingerprints; it does not store raw proxy URLs.
  A `suspect` entry excludes that saved proxy from future inspection-pool
  allocations, which is why an old `transport_timeout` could otherwise leave a
  usable proxy permanently unavailable.
- After an inspection-origin replacement's directed recheck, CPAM aggregates
  evidence **per new-proxy fingerprint**. A successful usage response or an
  upstream HTTP response (including 401/quota/rate-limit business responses)
  restores an already-suspect pool entry to `normal`. Local failures such as
  `credential_not_found`, configuration errors, cancellation, or no response
  are not transport proof and do not restore it.
- If any target sharing that same proxy reports explicit `ProxySuspect` in the
  same recheck batch, the suspect state wins regardless of result order; CPAM
  keeps it quarantined. This prevents one successful account from resurrecting
  a truly broken shared proxy.
- The replacement modal's unavailable-pool state exposes an admin-only
  **Restore N suspected proxy states and retry preview** action. It changes
  only Manager `proxy_pool_state` status rows, then regenerates the frozen
  preview. It never writes auth files, starts a quota refresh, changes billing,
  or creates a CPA inspection. Preserve
  `POST /v0/management/proxy-replacements/proxy-pool/restore-suspects` and the
  paired `usageServiceApi.restoreSuspectProxyPoolStates` call across upstream
  merges.

## 2026-10-19：持久化服务器错误降优先级队列

此项为 CPAM-only 二改。它绝不修改 CPA Core、`cli-proxy-api`、用量采集、计费、额度、历史请求或 `usage_events` 的既有字段/行；队列仅只读 `usage_events`，并只写 CPAM 自己的新增 SQLite 表。

未来“更新 CPAM”合并上游时，必须保留以下不可变行为：

- `server_error_priority_demotion_jobs`、`server_error_priority_demotion_cursor`、`server_error_priority_demotion_enabled_intervals` 是 `usage.sqlite` 的**加法迁移**；不得删除、重建、迁移走或用旧的内存 channel 替换。它们不包含 management key、Token、认证 JSON、代理 URL/凭据或失败原文。
- 旧的 `[server-error-priority] job queue full, dropped auth file ...` 丢弃路径已经废除。每个合格事件先由 SQLite `event_hash UNIQUE` 持久化；wake buffer 仅是通知，不是容量上限。满时允许合并 wake，绝不丢已确认事件。
- 默认 8 个 worker，环境变量 `USAGE_SERVER_ERROR_PRIORITY_DEMOTION_WORKERS` 限制为 `1..32`；`USAGE_SERVER_ERROR_PRIORITY_DEMOTION_WAKE_BUFFER` 默认 `8192`、限制为 `64..32768`。同一 credential 依据持久化 `credential_key` 严格 FIFO，不同 credential 可并发。
- 首次启动必须在 listener/collector 之前执行 queue `Bootstrap`，把当前最大 `usage_events.id` 设为不可回放 floor。老历史事件不能重放；新事件在 live fanout 或 reconciler 中恰好入队一次。
- 开关状态必须由 `AutomationRuntime.Reload -> SetEnabled` 先写入 enabled interval 再生效。关闭时 worker 暂停领取、已入队 backlog 保留；reconciler 仍可依据已关闭的 interval 推进 cursor 并跳过 disabled 时段，之后重新打开不得回放关闭期间产生的事件。
- 严格资格规则保持不变：HTTP `502`、`503`；HTTP `429` 仅 JSON `detail == "Rate limit exceeded"`；HTTP `500` 仅 Codex responses 请求标记加 `unexpected EOF` 或精确 `utls: dial upstream: proxy CONNECT returned status 407 Proxy Authentication Required`。其余 500/407/EOF 一律不降级。
- 每个事件都是一个独立 `-1` 工作，不合并为 `-N`。重试记录 `priority_before/priority_target`：当前值已等于 target 时完成 `already_at_target` 且不再 PATCH；当前值仍等于 before 时重放同一 target；否则重算既有 peer-rebalance 公式。仍排除自己、取同 provider 已启用 peer 最高优先级，最低 clamp 到 0。
- 继续复用 `MutationCoordinator`，并对同一 CPA setup 的全量 peer inventory 保留 20 秒 TTL、45 秒失败冷却和 in-flight single-flight；8 个 worker 不得同时发起同一个全量 inventory 请求。
- lease 过期只能转回 `retry_wait`，不得删除、不得减少 attempt；成功/ignored 保留 14 天后可清理，`pending/retry_wait/leased/failed` 不得按时间自动删。
- 新队列仅能重建/重启 CPAM 时随 CPAM 启动迁移；部署时仍严格遵循 CPAM-only 规则，禁止重启或重建 CPA Core / `cli-proxy-api`，禁止覆盖现有 `usage.sqlite`。


## 2026-10-19: Token Recovery CPA write-pressure containment

This customization is CPAM-only. It must never modify, restart, rebuild, or
reconfigure CPA Core / `cli-proxy-api`, and it must not query quota, usage,
history, billing, or scheduling endpoints as part of credential recovery.

- Token Recovery now uses its own `cpaauthfiles.Client` budget of **4 minutes**
  (`tokenrecovery.DefaultCPAAuthFileOperationTimeout`). Normal panel/auth-file
  list calls retain their existing 60-second budget. This prevents a busy CPA
  Core `/v0/management/auth-files` upload from being falsely reported as the
  generic `recovery_failed` after 60 seconds.
- Recovery keeps the existing 40-slot Token Acquisition concurrency, but CPAM
  limits the CPA credential mutation phase (`download current -> merge -> upload
  -> read-back verify`) to **4 concurrent accounts**. The per-file
  `MutationCoordinator` remains mandatory. This prevents a current-page/manual
  recovery burst from causing dozens of simultaneous CPA auth-file writes.
- A CPA auth-file operation reaching its context deadline is persisted as
  `cpa_auth_file_timeout`, never an empty generic `recovery_failed`. The
  redacted UI reason distinguishes initial/read timeout (nothing submitted),
  upload timeout (write result pending confirmation), and post-upload verify
  timeout (write may already have completed; refresh to verify).
- Do not reduce this recovery-specific 4-minute timeout or remove the 4-slot
  write gate during future upstream merges. Preserve the regression tests in
  `internal/service/tokenrecovery/service_test.go`. These controls only affect
  CPAM token replacement file I/O; they do not alter CPA request routing,
  downstream API billing, `usage_events`, quota collection, or priority
  demotion.


## 2026-10-19: Token Recovery outbound-acquisition boundary and diagnostics

This customization is CPAM-only. It must never modify, restart, rebuild, or
reconfigure CPA Core / `cli-proxy-api`, and it must not make quota, history,
billing, scheduler, or priority-demotion calls.

- The browser may still queue a current page of up to 40 manual credential
  updates immediately. The durable SQLite queue retains those jobs. The live
  CPAM `TokenRecoveryService` uses
  `RecommendedTokenAcquisitionWorkerCount = 12`, so no more than 12 external
  Token Acquisition flows run concurrently. This replaces the former live 40
  worker configuration; do not restore it during an upstream merge.
- CPA auth-file mutation remains separately limited to 4 concurrent accounts,
  and the per-file `MutationCoordinator` remains mandatory. The 12/4 controls
  do not change CPA scheduling, billing, quota, usage collection, or the
  24-hour-cooldown automatic-recovery behavior.
- Known outbound Token Acquisition transport failures must persist as
  `token_acquisition_transport_failed`, with only a fixed redacted operator
  message. Do not persist raw socket/DNS/TLS errors because they can contain
  proxy URLs or credentials.
- CPAM must distinguish CPA auth-file failure phases as
  `cpa_auth_file_read_failed`, `cpa_auth_file_upload_failed`, and
  `cpa_auth_file_verify_failed`; the UI may show only the phase and an HTTP
  status when available. CPA response bodies, tokens, management keys and
  proxy credentials must never enter task state, browser responses, or logs.
- Preserve the existing `cpa_auth_file_timeout` mapping and the four-minute
  auth-file operation budget. Existing historical `recovery_failed` records
  cannot be retroactively diagnosed because raw errors were intentionally not
  stored; they remain manually retryable.

## 2026-10-19: Visible priority synchronization after server-error demotion

This is CPAM-only. It is a read-only UI projection over CPAM's existing
durable server-error priority queue. It must never modify CPA Core,
`cli-proxy-api`, credential JSON, request routing, scheduling, usage events,
quota, history, or billing/price data.

- Preserve the authenticated Manager endpoint
  `GET /v0/management/server-error-priority-demotions/updates`. It must remain
  routed locally before the generic CPA management proxy and may return only
  the redacted completed `demoted` records: `id`, `fileName`, `authIndex`,
  `provider`, `priorityBefore`, `priorityTarget`, and `completedAtMs`, using
  the stable `(completed_at_ms, id)` cursor and the bounded terminal-index
  query. Do not expose failure bodies, account snapshots, proxy URLs, tokens,
  or management keys.
- The endpoint is strictly read-only against CPAM's
  `server_error_priority_demotion_jobs` table. It must not invoke a CPA
  auth-file read, CPA PATCH, quota/header/history/window-usage loading, token
  recovery, or any billing path. Keep
  `idx_server_error_priority_demotion_updates` as an additive SQLite migration.
- The accounts workspace must poll this narrow endpoint once per second only
  while the accounts view is active, the document is visible, the visible
  auth-file inventory is loaded, and a Manager scope is available. Preserve
  AbortController cancellation and the single-flight guard on view/scope
  changes. This polling path must never call `loadFiles()` or create a hidden
  full credential refresh.
- Apply a returned update only when the current local row matches exactly on
  physical `fileName`, normalized `authIndex`, and normalized `provider`, and
  its priority still equals `priorityBefore`. Update with `replaceFiles` only
  in that case. This guard is mandatory: it prevents a delayed queue event
  from overwriting a newer operator priority edit.
- Preserve the five-second startup overlap and tuple-cursor continuation so a
  just-completed demotion appears in the open credential list within seconds,
  without touching the existing priority queue execution semantics. During
  future upstream merges retain the endpoint, frontend API contract,
  `applyServerErrorPriorityUpdates`, `useServerErrorPriorityUpdates`, page
  wiring, and their tests.

## 2026-10-20: Recent nine-request anomaly filters

This customization is CPAM-only. It must never modify, restart, rebuild, or
reconfigure CPA Core / `cli-proxy-api`, credential JSON, request routing,
scheduling, `usage_events`, billing, quota collection, or priority demotion.

- The authenticated CPAM `account-recent-index` endpoint accepts an optional
  `recent_limit` only from **1** through **9**. Omission remains exactly one
  safe request record for normal global recent-request sorting.
- Credential-list operational filters `recent_server_overloaded_502` and
  `recent_failures` request nine safe indexed request records globally and
  apply display-only matching locally with a threshold of two.
- The strict overload filter requires all three facts: `failed=true`, HTTP
  `502`, and the complete safe diagnostic token `server_is_overloaded` in
  `header_error_code` or `fail_summary`. Generic 502, EOF, proxy 407,
  500/503/429, and prose-only matches are excluded.
- This path reads only CPAM SQLite evidence. It must not invoke CPA Core,
  trigger `account-history` catch-up, mutate credentials, refresh quota or
  Credits, alter billing/price data, or enqueue priority work.
- Preserve 1,000-target batching, bounded concurrency, AbortController
  cancellation, and coverage gating. A partial, failed, stale, or compact
  one-record index must never be interpreted as a completed empty anomaly
  result; the UI must show the dedicated loading/error state instead.

## 2026-10-21: High-concurrency credential-list evidence stabilization

This customization is CPAM-only. It must never modify, restart, rebuild, or
reconfigure CPA Core / `cli-proxy-api`; it must not change credential routing,
Token Acquisition behavior, proxy replacement, inspection scheduling,
`usage_events` writes, quota semantics, Credits, billing, or model pricing.

- The global `account-recent-index` read path must use the existing set-based
  collection query for both indexed and non-indexed databases. Do not restore
  the old one-query-per-credential indexed branch: it caused a 4,000+ account
  list to monopolize SQLite while preserving no extra UI semantics. Keep the
  current global inventory, global recent-request sort, legacy identity rules,
  and safe/redacted response fields unchanged.
- A canceled or deadline-expired `account-window-usage` projection read must
  return its cancellation to the HTTP layer. It must **not** fall back to the
  raw `usage_events` table, which can be tens of gigabytes and turns a normal
  browser AbortController cancellation into an expensive 500/timeout. A truly
  unavailable projection (not cancellation) retains the existing raw fallback
  behavior and calculation semantics.
- The original passive Header/inspection-only cadence is superseded **only** by
  the explicitly scoped 2026-10-22 live-refresh contract below. Do not restore
  a full-inventory history/window polling fan-out; keep Header/inspection
  refresh independent from the current-page streams.
- Server-error priority-demotion UI updates must retain the strict existing
  `(fileName, authIndex, provider, priorityBefore)` guard, but merge a batch
  through one normalized credential-key index in `O(files + updates)` rather
  than repeated full-array `findIndex` scans. The one resulting React file-list
  replacement is scheduled with `startTransition`; polling cadence, durable
  queue behavior, and every individual `-1` demotion rule remain unchanged.
- Visible-page recovery metadata must retain a matching current row's compact
  `recoveryTask` while a parent seed is rebuilt or a recovery lookup is in
  flight. The normal list still uses only the redacted batch recovery route and
  never fetches raw credential JSON. No-op semantic maps must not trigger a
  state replacement, so `凭证已更新` / `已自动更新一次` badges do not blink or
  cause avoidable card rerenders.
- For a replacement list-window request that fails on the same visible page,
  retain the prior matching window data; on page/filter change, retain no
  mismatched old-page evidence. Stale/aborted requests remain unable to commit
  results. This is display-cache behavior only and never writes or recalculates
  accounting data.

## 2026-10-22: Live visible-page request refresh without UI fan-out

This customization is CPAM-only. It must never alter CPA Core,
`cli-proxy-api`, request routing, credential files, Credits, billing/price
calculation, `usage_events` writes, inspection scheduling, or priority
demotion execution.

- Keep the full CPA auth-file inventory and browser-side global filtering.
  For **only the current visible page** (normally at most 50 rows), poll the
  read-only Manager `account-recent-index` endpoint every **10 seconds** with
  `recent_limit: 9`. Its result drives the card's latest-request timestamp,
  nine request bars, and failure tooltip data. Do not replace this with a
  full-inventory history request.
- Poll current-page `account-history` and `account-window-usage` every
  **60 seconds**. History continues to provide request/token/cost/success-rate
  totals; window usage continues to provide the existing quota-window cost and
  totals. These are display-only SQLite reads and must never issue a CPA quota
  probe or mutate accounting.
- The global compact `account-recent-index` remains the source for full-list
  `recent` sorting. Refresh it every **30 seconds only while the active sort
  key is `recent`**; retain the initial index load and explicit workspace
  refresh behavior for compatibility. Recent-nine anomaly filters keep their
  independent complete-global coverage path and must not be downgraded to the
  current-page overlay.
- All three streams are visibility-gated and context-aware single-flight.
  A timer tick must not abort/restart an identical slow current-page request;
  cancel only when page/connection/target context changes. Header snapshot
  refreshes must not alter the list-window request identity merely because
  mutable Header timestamps or percentages changed. This prevents the
  `account-window-usage` cancel/restart storm and preserves responsive list
  controls under high traffic.

## 2026-10-22: Accounts-list live-refresh gate

This customization is CPAM-only. It controls only browser-side display reads;
it must never alter CPA Core, `cli-proxy-api`, credential files, proxies,
request routing, scheduler behavior, `usage_events`, Credits, billing/pricing,
quota mutation, Token Acquisition, token recovery, inspection execution, or
server-error priority-demotion execution.

- The credential-list toolbar owns the browser-local preference
  `accountsPage.liveRefreshEnabled`, defaulting to `true`. It persists per
  browser and shows a compact frozen-snapshot indicator when off.
- When disabled, only passive Accounts UI reads stop: the visible-page
  10-second recent-nine request index, visible-page 60-second history/window
  projections, conditional 30-second global recent-sort index, passive
  Header/inspection refresh, and the UI projection of completed priority
  demotions. In-flight display reads are aborted or invalidated so a late
  response cannot redraw a frozen list.
- Initial/context reads (page, filter, sort, connection changes) and the
  explicit toolbar refresh remain one-shot reads while frozen. Re-enabling
  performs one bounded catch-up for the visible page and, only when sorted by
  recent request, the global compact index. It must not call a quota refresh,
  CPA write, Token Acquisition, recovery, or Provider API.
- `useServerErrorPriorityUpdates` accepts a separate `pollingEnabled` gate.
  A user pause retains only the last acknowledged tuple cursor and resumes from
  it; an inactive/hidden/unloaded lifecycle or Manager-scope change resets the
  cursor. Preserve its existing AbortController, single-flight, and strict
  identity/priority guards.
- Future upstream merges must retain this control and its tests. Do not turn
  it into a backend scheduler switch or couple it to billing, quota, account
  health, automatic recovery, or any CPAM/CPA mutation path.

## 2026-10-23: Copy selected authorization-file JSON

This customization is CPAM-only. It must never modify, restart, rebuild, or
reconfigure CPA Core / `cli-proxy-api`; it must not modify credential files,
proxies, request routing, scheduling, inspection, Token Acquisition,
automatic recovery, priority policies, `usage_events`, Credits, billing, or
quota accounting.

- The Accounts floating selection toolbar's **More** menu contains **Copy
  authorization file data**, immediately before the existing batch-download
  action. Batch download and every other action remain intact.
- The browser submits only the minimum identity tuple to the authenticated
  Manager endpoint `POST /v0/management/auth-files/export-selected`:
  `clientKey`, `fileName`, `authIndex`, `accountEmail`, `provider`, `runtimeId`,
  `accountId`, and `accountSnapshot`. It must never send raw authorization
  JSON, tokens, headers, secrets, or `proxy_url`.
- The endpoint is handled by CPAM before the generic CPA management proxy,
  requires normal panel authorization, and sends `Cache-Control: no-store` on
  every response. It accepts a strict whitelist-only JSON request; unknown or
  duplicate request fields are rejected.
- Manager resolves the saved CPA setup itself, snapshots CPA auth-file
  inventory once, identity-verifies every logical selection, de-duplicates
  physical source-file reads, and limits those reads to **8** concurrent jobs.
  It permits at most **300** logical selections and refuses final record arrays
  larger than **48 MiB**. It makes no mutation, cache invalidation, database
  write, scheduler call, or raw-credential log entry.
- A selected member of a shared JSON array must export exactly that one
  verified object, never its siblings. Ambiguous, missing, malformed,
  runtime-only, or identity-mismatched sources produce only a safe
  `fileName + fixed error code` outcome.
- The normal UI serializes only returned `records` as one top-level JSON
  array and copies it directly to the clipboard. It does not trigger
  `loadFiles`, quota refresh, history, recent-request, window-usage,
  inspection, recovery, or priority requests. An in-flight guard prevents
  duplicate export clicks.
- On partial success the clipboard still receives successful records while the
  result dialog retains only safe failure names/codes. Raw JSON is retained in
  browser memory only when clipboard writing fails; the fallback dialog offers
  retry-copy/download and clears the JSON state when it closes or retry-copy
  succeeds. Do not add localStorage, URL state, analytics, logs, or SQLite
  persistence for this data.
- Preserve the endpoint, `cpaauthfiles` single-record extractor,
  `authfilesexport` service, identity-only web target builder, result modal,
  locale keys, and their tests during every future upstream merge/update.

## 2026-10-23：独立代理过载统计（必须保留）

这是 CPAM-only 的只读诊断二改，后续“更新 CPAM”合并上游时必须保留，不能改写为 CPA Core 功能，也不能触碰计费、Credits、额度、调度、巡检或优先级 worker。

- 凭证列表工具栏保留原有“代理 IP 筛选/保存代理池”，另外提供独立“代理过载统计”按钮和弹窗；两条功能的状态、接口和 UI 不得互相替换。
- 统计范围固定为当前 CPA 清单中最近 72 小时有请求的账号，不按启用/禁用筛选；每个账号只检查最新 9 条请求，并按账号当前 `proxy_url` 归属。`proxy_url` 仅去除首尾空白及末尾连续 `/` 用于分组，展示/复制保留该组第一个当前原始 URL。
- 每次计算只允许一次 `GET /v0/management/auth-files?include_proxy_url=1` 投影清单；禁止扫描原始认证 JSON、`/download`、`credential-runtime-metadata`、逐账号 CPA 请求或浏览器端聚合。CPA 清单失败必须安全失败，不能回退到文件扫描。
- SQLite 只读路径 `RecentProxyOverloadEvidence` 按最多 500 个目标分批（5000 个账号最多约 10 次集合查询），要求最新请求索引存在；禁止 N+1、写入/迁移 `usage_events` 或返回账号、文件名、邮箱、原始错误体、Trace、Token。
- “服务过载”精确条件固定为 `failed=true`、HTTP `502`，且 `header_error_code` 精确（不区分大小写）为 `server_is_overloaded`，或 `fail_summary` 含有 ASCII 边界完整 token `server_is_overloaded`。普通 502、EOF、500、503、429、407、401、`not_server_is_overloaded` 均不得计入。
- 服务端仅返回代理级聚合：活跃账号数、无代理数、代理数、过载账号数、每代理有/无过载及比例；响应 `Cache-Control: no-store`，错误只返回静态安全类别。进程内完整结果缓存 15 秒并共享进行中的计算；首次打开和用户点击“刷新统计”才请求，不能接入凭证列表自动刷新轮询。
- 前端弹窗筛选、全选当前可见代理、LF 分隔复制均为本地操作；刷新失败时保留上一份完整表格。打开弹窗不得触发 `loadFiles`、history、window-usage、runtime metadata、额度或 Header 请求；关闭/连接切换只取消该统计请求。


## 2026-10-23: Saved-proxy overload difference and success-rate sort

This CPAM-only extension must be preserved during every future upstream merge.
It is display-only and must never change CPA Core, cli-proxy-api, credential
files, request routing, scheduling, inspection, Token Acquisition, recovery,
usage_events, Credits, billing/pricing, quota accounting, or priority work.
Future merges must retain it together with the pre-existing selected
authorization-file JSON export flow.

- After the existing one-shot CPA `auth-files?include_proxy_url=1` inventory
  projection and set-based SQLite evidence aggregation, read the Manager
  Server's saved proxy URL list exactly once. Never scan raw credential JSON,
  download files, call credential-runtime-metadata, use one request per proxy,
  or add CPA Core reads/writes.
- Normalize saved and observed URLs by trimming whitespace and trailing `/`.
  `unobserved_proxies` means a saved URL has no active account with a request
  in the last 72 hours; it is not a failure, 502, disabled, or health verdict.
  If the saved list cannot be read, return `saved_proxy_list_available=false`
  and never represent the unknown result as zero.
- Keep the API fields `saved_proxy_count`, `observed_saved_proxy_count`,
  `unobserved_proxy_count`, and `unobserved_proxies`. The modal's `unobserved`
  view supports local select-all and LF copy only; it must not mutate the
  proxy pool, replace a proxy, rerun inspection, or change account state.
- Success rate is strictly `non_overloaded_account_count / active_account_count`
  (zero denominator renders `0%`). Its column sorts locally ascending or
  descending; ties retain the prior deterministic aggregate order. Sorting,
  filtering, selecting, and copying must not refresh or make network requests.
- A successful saved-list write must invalidate the 15-second proxy-overload
  cache. Older in-flight generations must not repopulate that cache after the
  invalidation. This only affects the read-only statistic.

## 2026-10-27: Token Recovery terminal-state durability and stale-task fence

This CPAM-only fix must be preserved during every future upstream merge. It
does not modify CPA Core / `cli-proxy-api`, credential JSON contents, request
routing, usage events, Credits, quota accounting, pricing, or billing.

- `internal/service/tokenrecovery` now persists the terminal result after the
  external Token Acquisition and CPA auth-file mutation have finished. A
  transient SQLite `BUSY`/`LOCKED` result retries only the terminal row update;
  it never repeats Token Acquisition, CPA upload, or credential mutation.
- Terminal writes use a cancellation-independent bounded context (30 seconds)
  and four bounded backoff attempts. Non-lock/semantic errors are not retried.
  The task remains an explicit manual-review failure if durable persistence
  still cannot be completed, with a redacted log message.
- Every claimed recovery has a 60-minute end-to-end deadline. Cancellation or
  deadline produces `recovery_timeout` / manual-only terminal state and never
  starts a second external request. A one-minute reconciler terminalizes older
  `auto_running`/`manual_running` rows as `recovery_timeout`; it never requeues
  or replays them.
- On process start, the existing startup fence remains in force: interrupted
  in-flight rows become manual-only. The new fence/reconciler writes use the
  same bounded SQLite-lock retry path, so a restart cannot silently leave a
  row permanently `*_running` solely because its final state update hit a
  busy database.
- The regression set is mandatory: terminal success/failure lock retry without
  a second acquisition, per-task timeout without CPA upload, stale-row
  reconciliation, and repository state isolation. Run the manager-server tests
  in Linux isolation before promotion; Windows permission/signal-only failures
  are not release evidence.
- A production repair may terminalize only an explicitly verified stale-row
  allowlist (`id + file_name + started_at_ms + status/mode`), writing a
  pre-image audit file under `/home/cpam/backups/`. It must not touch
  `/home/cpam/data/usage_events*`, usage snapshots, `data.key`, CPA Core, or
  billing tables.

## 2026-10-28: Pricing-rebuild-safe account-history reads

This CPAM-only performance fix is mandatory to preserve during future updates.
It changes only read-path behavior while `pricing_v1` is rebuilding; it does
not alter billing writes, Credits, quota accounting, usage events, pricing
rows, CPA Core, `cli-proxy-api`, or scheduler behavior.

- `account-history` first reads durable projected/core rows and the stored
  pricing prefix. It must not scan the full raw `usage_events` tail while
  pricing state is `rebuilding` or `clearing`; that tail scan was the source
  of multi-minute page stalls/timeouts on large databases.
- During an incomplete rebuild, request counts and token totals remain
  available from immutable core/projection data. `total_cost` may be `null`
  for accounts whose pricing coverage is not durable yet; the UI must render
  this as unavailable/synchronizing, never as zero or monthly exhaustion.
- Covered pricing rows remain visible during rebuild. Expensive raw-tail
  recovery is allowed only after the pricing state is no longer rebuilding or
  clearing, and any later recovery query must be restricted to requested
  account keys.
- A frontend refresh, pagination change, or service restart must not enable a
  writer/derivation worker or mutate the usage database as a performance fix.
  Preserve `USAGE_BACKGROUND_DERIVATIONS_ENABLED` and the existing server
  maintenance policy; billing correctness takes precedence over displaying a
  temporarily incomplete cost field.
- Release verification must include focused repository/store/monitoring tests,
  a bounded multi-account `account-history` request, service health, binary
  hash, and confirmation that `cpa.service`/CPA PID did not change.

## 2026-10-28：凭证列表价格显示恢复（B 方案，必须保留）

本条覆盖上一节“重建期间禁止逐账号价格恢复”的展示降级规则。用户明确选择
B 方案：凭证列表当前页必须恢复旧版精确价格读取，即使 `pricing_v1` 处于
`rebuilding` 状态。该改动仍只属于 CPAM 读路径，不改变 CPA Core、
`cli-proxy-api`、usage events、Credits、计费写入、模型价格写入或调度。

- `Store.LoadUsagePricingAccountSnapshot` 必须使用既有
  `UsagePricing.LoadAccountRowsTx`，不能退回仅读取 durable prefix 的
  `LoadAccountRowsForHistoryTx`。
- 价格覆盖不完整时，必须调用既有的
  `LoadAccountRowsFromEventsTx`，按当前请求账号从兼容 monitoring projection
  加 projection watermark 之后的 `usage_events` 尾部精确补算；覆盖校验仍是
  唯一准入条件。
- 可恢复账号返回真实 `total_cost`；无法证明覆盖完整的账号继续返回 `null`，
  不得估算、复用旧 revision 或显示为 0。`sync_status=pending` 可以继续表示
  全局 checkpoint 追赶，但不能阻止非空金额渲染。
- 该路径是只读的。不得通过刷新、翻页或重启启用全量派生 worker，也不得把
  本次 B 方案改成未经确认的缓存、排队或旧价格方案。
- 回归测试必须覆盖 `pricing_v1=rebuilding` 时逐账号金额恢复且读取不写表；
  部署验收需记录 50 账号页响应时间、CPAM 健康、二进制 SHA-256，并核对
  `cpa.service` PID 未变化。

## 2026-10-28：账户历史价格缓存与后台精确重算

本次 CPAM 二改用于解决 `POST /v0/management/monitoring/account-history` 在模型价格重建或价格覆盖不完整时同步扫描大量 `usage_events`，导致凭证列表等待、按钮卡顿和价格长期显示“同步中”的问题。该优化只属于 CPAM 派生读模型，绝不改变计费事实来源。

- 正常账户历史请求只读取当前 revision 的持久化价格 rollup、CPAM 核心账户汇总、模型价格和 `usage_pricing_account_price_cache_v1`；禁止在 HTTP 请求路径调用 `LoadAccountRowsFromEventsTx`。
- 新增 CPAM SQLite 派生表 `usage_pricing_account_price_cache_v1`，按 `(account_key, pricing_revision)` 保存 `total_cost`、`covered_event_id`、`updated_at_ms`、`status` 和错误说明。它是可丢弃缓存，不是计费源，也不替代 `usage_events`、Credits、quota 或 CPA Core 状态。
- 价格覆盖完整时响应 `price_state=fresh`；覆盖不完整但有上次精确值时立即返回该值并标记 `price_state=stale_repricing`；没有缓存时 `total_cost=null`、`price_state=unavailable`。前端只显示非阻塞提示，不阻塞请求/额度/计费刷新。
- 不完整账户由 CPAM `AccountPriceCacheWorker` 去重排队，单批最多 25 个、内存队列上限 5000、单 worker 并发 1，SQLite 忙/锁按 250ms/1s 重试；精确重算完成后原子写回缓存，下一次可见页刷新即可显示。
- `USAGE_ACCOUNT_PRICE_CACHE_ENABLED` 独立于 `USAGE_BACKGROUND_DERIVATIONS_ENABLED`，默认开启；即使为避免历史维护锁竞争而关闭通用派生 worker，账户价格缓存 worker 仍可单独运行。关闭该开关时只显示已有缓存/不可用状态，不启动精确重算。
- worker 只读保留事件/原始事件并计算派生价格，不写 `usage_events`、Credits、usage rollup 核心事实、CPA Core 或调度状态。失败只记录缓存 `status=failed`，保留上次 `total_cost`。
- exact repricing 在写入 `fresh` 前必须把 retained/raw pricing rows 与 core account rollup 做覆盖校验；projection watermark 不能单独证明完整性。覆盖不完整时 fail-closed，不得发布低估的“新鲜”价格。
- 重启后队列可丢弃；下一次账户历史读取会再次为不完整账户入队。任何后续“更新 CPAM”合并必须保留上述 HTTP 无 raw scan、缓存字段、队列边界和计费隔离约束。

## 2026-10-28: Superseding amendment - upstream pricing path and refresh matrix

The preceding account-price-cache section is historical and is superseded by this amendment. The custom `AccountPriceCacheWorker`, HTTP `price_state` fields, price cache queue, and `USAGE_ACCOUNT_PRICE_CACHE_ENABLED` configuration are retired. Do not restore them during a future upstream merge. The SQLite cache table and migration may remain only as inert rollback/schema compatibility; no runtime reader, writer, queue, or UI marker may use it.

The active CPAM account-history path is the upstream exact path:

- `Store.LoadUsagePricingAccountSnapshot` calls `LoadAccountRowsTx` and uses the existing coverage-checked `LoadAccountRowsFromEventsTx` recovery only for requested incomplete account keys.
- Responses expose only upstream `sync_status` and exact `total_cost`; they must not emit or render `price_state`, `price_revision`, or repricing footnotes.
- This is a read/derived-display change only. Never modify `usage_events`, Credits, usage rollups, CPA Core, scheduler state, or billing writes.

The active frontend account-history refresh scope is explicit:

1. Initial list entry: all inventory accounts and related history/cost data.
2. Manual page change or filter change: current visible page only; cancel the obsolete current-page request, but do not cancel an all-inventory request.
3. Enabling live refresh: one all-inventory history refresh first.
4. Live refresh intervals: current visible page only. Recent-request index and quota/window usage retain their independent refresh periods.
5. Single-account manual refresh: that account only.
6. Top-right full refresh: all inventory accounts and related data.

Use separate abort/deduplication state for `all`, `current`, and `single`. Preserve the 240000ms usage timeout and never silently fall back to a full read from a current-page refresh.

For an `all` history refresh, retain complete-inventory coverage but put the
currently visible page (and an explicitly opened row outside that page) at
the front of the request order. Publish every completed 200-account response
to the UI immediately; do not wait for the final background batch before
rendering an exact `total_cost` for the visible row. This is a frontend
read/render scheduling rule only: it must not increase request concurrency
above the existing two bounded history requests, enable a writer, alter
billing data, or change the `all/current/single` scope matrix.

## 2026-10-28: Priority-demotion feature flag is explicitly enabled

The production native CPAM service at `/home/cpam` currently runs with
`USAGE_SERVER_ERROR_PRIORITY_DEMOTION_ENABLED=true` in
`/home/cpam/config/cpam.env`. The flag is environment-locked, so the panel
must report it as enabled/locked and future CPAM updates must preserve the
setting across binary replacement and `cpam.service` restarts. Do not modify
CPA Core, `cli-proxy-api`, `/home/cpa`, or billing data when preserving this
flag.

## 2026-10-29: v1.14.10 / PR #1083 price-recovery merge

This CPAM-only upstream merge preserves the custom credential-list UX and
imports upstream historical-price recovery exactly where it belongs.

- usagepricing.retainedEventSourceTx and retainedAccountEventSourceTx must
  treat intact raw usage_events as authoritative. A compatible retained
  projection may contribute only archive rows proven deleted through
  usage_archive_event_refs.raw_deleted_at_ms. Never revert to projection
  prefix plus raw tail; that can omit a live event whose projection entry is
  stale or missing.
- Preserve the current custom AccountsPage.tsx and account-history refresh
  scope matrix. The upstream pricing recovery is a server-side read-path fix;
  it must not reintroduce an all-inventory raw-event scan for a normal
  current-page history request.
- This change is read-only with respect to billing facts. Do not modify
  usage_events, Credits, pricing-write semantics, CPA Core, cli-proxy-api,
  /home/cpa, or cpa.service when updating CPAM.
- Fast release evidence used for this merge: focused usagepricing Go package
  test, web TypeScript type-check, and production web build. Do not substitute
  a full browser suite for the bounded price-recovery regression unless a
  later change touches that UI path.

## 2026-10-29: Current-page-only account-history refresh (A decision)

This amendment supersedes the preceding `all/current/single` account-history
refresh matrix. The operator explicitly chose option A because the 17.4 GB
usage database and high-concurrency account inventory made automatic full
history/pricing reads unusable. It is mandatory to preserve this behavior in
future CPAM updates.

- `POST /v0/management/monitoring/account-history` is never automatically
  called with the full credential inventory. Initial entry, pagination,
  filtering, live-refresh enablement, live-refresh ticks, and the top-right
  refresh request only the current visible page (normally 10 rows, or the
  configured page size) plus an explicitly opened row outside that page.
- A single-account manual refresh remains a single-target request. All history
  scopes cancel obsolete page requests and use the existing bounded history
  concurrency; no new global queue or pricing worker may be introduced.
- The full `auth-files?include_proxy_url=1` inventory load and global compact
  `account-recent-index` request remain unchanged. Global recent-request sorting
  therefore remains available without loading global historical cost data.
- `account-window-usage` remains a current-page stream with its existing
  independent 60-second cadence. Recent-request bars and failure reasons keep
  their current-page cadence. These streams must not trigger a full
  `account-history` request.
- Exact historical totals and `total_cost` still use the upstream coverage-
  checked read path for the requested keys only. This is read-only and must not
  alter `usage_events`, Credits, usage rollups, pricing writes, scheduler state,
  CPA Core, `cli-proxy-api`, or billing behavior.
- Keep the management/usage HTTP timeout at 240000 ms. Do not restore the old
  initial/full-history pass, full-refresh revision, or any UI behavior that
  silently expands a current-page request into an inventory scan.

## 2026-10-30: Durable Token Recovery display and CPA upload-timeout reconciliation

This CPAM-only correction must survive every future upstream merge. It does
not change CPA Core / `cli-proxy-api`, credential scheduling, proxy routing,
priority handling, usage events, Credits, pricing, billing, quota collection,
Token Acquisition concurrency, or the eight-slot CPA credential-write gate.

- The normal credential list obtains only the current visible Codex page's
  redacted `token_recovery_tasks` state through one bounded Manager SQLite
  read. It must not download credential JSON or make one SQLite lookup per
  row. Returned states remain positional by `clientKey` and contain no token,
  API key, credential JSON, or proxy secret.
- Display matching order is exact `(file_name, auth_index, account_email)`,
  then same physical file plus `auth_index`, then a **unique** same
  file/provider/email fallback. The final fallback is display-only: it must
  never be used to queue recovery, select a write target, or mutate CPA. If
  two durable tasks match that file/email, return no fallback state rather
  than attach a label to the wrong credential.
- `succeeded`, failed, and automatic-attempt labels are durable state from
  `token_recovery_tasks`, not browser-local markers. A historical succeeded
  task must render normally but must not trigger a credential-list reload.
  Only a task first observed as queued/running in the same browser session and
  then observed as `succeeded` may perform one deduplicated target-file
  reconciliation reload.
- A CPA auth-file **upload** timeout is ambiguous: Core may have applied the
  uploaded bytes after CPAM's client deadline. After the original write lock
  and eight-slot write gate are released, CPAM may perform at most three
  short, read-only downloads and verify the exact in-memory merged token
  result. It must never issue a second Token Acquisition request or a second
  upload. Verified bytes complete the durable task as `succeeded`; an
  unverified outcome remains the existing redacted `cpa_auth_file_timeout`
  terminal state for manual review.
- Every recovery state mutation that changes queue ownership or a terminal
  result now acquires the shared CPAM SQLite **critical** writer lease before
  opening its transaction. Terminal `succeeded`/failed transitions therefore
  serialize with raw usage-event persistence instead of racing it under an
  80-worker recovery burst. SQLite `BUSY`/`LOCKED` retries continue for the
  entire cancellation-independent 30-second terminal-write budget with an
  exponential backoff capped at one second; semantic failures do not retry,
  and no retry can repeat Token Acquisition, a CPA upload, or another
  credential mutation.
- Once `POST /v1/tokens` has returned a `batch_id`, a transient transport
  failure while polling that known batch is no longer terminalized as
  `token_acquisition_transport_failed`. CPAM keeps idempotently polling that
  same batch until the existing acquisition deadline, using capped backoff and
  **never issuing a second POST**. A transport failure before a batch ID is
  still unknown and remains a redacted manual-review failure.
- Preserve focused coverage for stale-auth-index display, ambiguous-email
  fail-closed behavior, historical-success no-reload, pending-to-success
  one-time reload, timeout-after-applied-upload reconciliation, known-batch
  polling transport recovery, terminal retry beyond four lock collisions, and
  shared-writer lease serialization.

## 2026-10-30: Token Recovery 成功后立即退出“需重新授权”筛选

This CPAM-only UI reconciliation rule is mandatory during every later upstream
merge. It does not change CPA Core / `cli-proxy-api`, token acquisition,
credential bytes, proxy routing, inspection execution, request routing,
usage events, Credits, quota, pricing, billing, or priority scheduling.

- A durable `token_recovery_tasks.status=succeeded` row is an authoritative
  successful credential-write boundary for the exact current Codex credential.
  The Accounts reauth filter must immediately retire stale raw-status,
  inspection, Header, and prior recovery-action authentication evidence at or
  before `completed_at_ms`; the row must therefore leave `需重新授权` without a
  second Token Acquisition request, CPA write, or full credential inventory
  reload.
- This applies both to a queued/running task observed becoming successful in
  the open browser and to an already-successful historical task first returned
  after the page opens. Historical success remains a bounded display-state
  update only; it must not cause a credential reload.
- The recovery dialog forwards the durable task and its completion timestamp
  to Accounts. The timestamp must be preserved instead of substituting the
  browser clock. Install the status boundary before a target-file reload and
  install it again against the reloaded target, so an old raw 401 arriving in
  that reload cannot re-enter the filter while the reload is in flight.
- Fail closed for a newer real failure: a raw/request/inspection 401 whose
  evidence timestamp is later than the successful task completion must remain
  in `需重新授权`. Do not turn this UI rule into a blanket status clear.

## 2026-10-30：独立派生 SQLite 存储（方案 2，设计已确认）

LO 已确认 CPAM 采用“主事实库 + 独立派生库”方案，后续实现/更新必须保留本节约束。完整设计与部署计划：

- `docs/superpowers/specs/2026-10-30-separate-derived-sqlite-design.md`
- `docs/superpowers/plans/2026-10-30-separate-derived-sqlite.md`

核心边界：

- 主库 `/data/usage.sqlite`（服务器对应 `/home/cpam/data/usage.sqlite`）仍是唯一计费事实源；保留 `usage_events`、identity ledger、model prices、Credits、额度、归档、Token Recovery、代理、巡检和优先级状态。
- 新的 `usage-derived.sqlite` 只保存可重建的 account-history、pricing、dashboard hourly、monitoring 等物化结果、coverage/generation/checkpoint 与派生索引；不保存 Token、认证 JSON、管理 Key、代理 URL 或原始错误正文。
- `usage_event_identity_ledger` 和 `usage_archive_*` 不得搬到派生库；派生库使用自己的 applied-event ledger 和 source epoch。
- 派生 Worker 必须先用主库只读连接读取有界事件批次，结束主库事务后再写独立库；禁止 `ATTACH`、跨库写事务、长主库快照和 HTTP 请求内同步全量 catch-up。
- `USAGE_DERIVED_STORAGE_MODE=primary|separate` 默认为 `primary` 兼容旧版；`separate` 模式下绝不启动旧的主库派生 Worker/maintenance。`USAGE_BACKGROUND_DERIVATIONS_ENABLED` 只控制 Worker 是否运行，关闭后仍可读取最后已验证的独立派生快照。
- 读路径合并派生库已验证 coverage 与主库水位线后的短尾部；price revision/generation/source epoch/archive proof 不一致时必须标记 stale/pending 或 fail-closed，不能用旧金额冒充 fresh，也不能清零已知金额。
- 归档删除前必须验证独立派生库覆盖待删 raw event 范围；覆盖不足、独立库损坏或 epoch 不匹配时拒绝删除原始数据。

当前实现边界（必须如实保留）：

- 独立 Worker 已实际接管 `account-history` 和 `pricing` 两条流，包括水位线、价格 revision/generation、归档 source 合并与当前页有界 tail。
- `dashboard-hourly` 和 `monitoring-*` 的独立 schema 已预留，但本版未将旧的统计转换逻辑写入新库；`separate` 模式下这些读取会有界回退到原始事件查询，不会读旧主库派生表冒充新数据。
- 因此归档清理在所有必要 stream 完成覆盖前会 fail-closed；不能为了释放磁盘空间跳过这个保护。
- 可见页尾读超过 5,000 事件间隔时不同步扫描整张历史，保留最后已验证快照并显示 pending/stale，由后台 Worker 追赶。
- 首次创建独立库时，只在独立库导入主库中已验证且结构版本匹配的 legacy history/pricing 物化行作为安全起点；不复制原始事件或控制表。
- 独立库打开、schema 校验或 legacy bootstrap 失败时，CPAM 不偷偷回退到主库派生写入；服务进入 separate/degraded 状态，原始采集、计费事实与控制面继续运行，派生读与归档删除保持 pending/fail-closed。
- `CatchUpAccountHistoryRollups` 在 separate runtime 不可用时必须显式返回 `Pending=true`，不得用零水位把 degraded 状态伪装成已追平；归档删除 guard 在进入删除阶段、每个批次开始和原始删除前再次校验，尽量缩小跨库检查与不可逆删除之间的窗口。
- `usage-derived.sqlite` 是独立部署产物，快照/回滚时必须与其 `-wal`/`-shm` 一起单独备份并保留路径配置；恢复 `usage.sqlite` 时不得把旧/损坏的派生文件误当成同一 source epoch，必要时删除派生库让 CPAM 重新 bootstrap，但绝不覆盖主库或 `data.key`。

- 所有 CPAM 更新、构建和部署仍只改 CPAM；服务器只重启 `cpam.service`，绝不触碰 CPA Core、`cli-proxy-api`、`/home/cpa` 或 `cpa.service`。部署前后核对 CPA Core 身份、主库、`data.key`、secrets、OpenResty 和证书配置。
- 实施顺序固定为：配置/只读源边界 → 独立 schema → 有界 source/sink → 双库 engine → Store 读路径 → 归档 guard → Worker/status → 前端金额显示 → 本地验证 → 服务器 staged（先 paused，再 enabled）部署。失败时可切回 `primary + USAGE_BACKGROUND_DERIVATIONS_ENABLED=false`，不恢复或覆盖主库。

## 2026-10-31：独立 pricing 追赶、当前页精确金额与真实 lag（必须保留）

本节补充并优先于上文中任何“独立 pricing 未追平就让当前页金额为空”的表述。它只改变
CPAM 的可重建派生调度与**只读**展示路径；绝不改变 `usage.sqlite` 事实、`usage_events`、
Credits、额度、模型价格写入、归档事实、CPA Core、`cli-proxy-api`、`/home/cpa`、`cpa.service`
或任何计费写入语义。

- 独立 runtime 的 pricing 快车道配置必须保留：
  `USAGE_DERIVED_PRICING_CATCHUP_BATCH_SIZE`（默认 `1000`）、
  `USAGE_DERIVED_PRICING_CATCHUP_MAX_BATCHES`（默认 `4`）、
  `USAGE_DERIVED_PRICING_CATCHUP_LAG_EVENTS`（默认 `10000`）。服务器可用受控值
  `2000 / 8 / 10000`。pricing 的 generation/epoch/pricing revision/archive generation
  不一致，或真实 lag 达阈值时，每轮最多先跑一个普通 account-history 批次，再跑有上限的
  pricing burst；仍只有一个 derived SQLite writer。
- `derivedstore.ApplyBatch` 必须携带完整 source snapshot 的 `SourceTargetEventID`。它与
  `ToEventID` 分离：后者只能推进已提交 coverage，前者只记录当轮主库真实 target。禁止再把
  小批的 `ToEventID` 写成 target，造成“`ready + lag=0`”的伪完成。
- `Store.SeparateDerivedStatus` 每次读取状态都必须通过 runtime 的 `mode=ro + query_only=ON`
  source snapshot 刷新公开 target；HTTP `/status?scope=database-maintenance` 在
  `coverage < target` 时必须显示 `catching_up` 和真实 `lagEventId`。
- **B 方案在 separate runtime 也必须成立：**`LoadUsagePricingAccountSnapshot` 先合并当前页账号的
  有界可见 tail；仅“全局 pricing stream 尚未追平”只能让 `Available=false`/UI 显示同步中，**不能**
  触发主库 pricing 回退。只有 `accountPricingCoverageIncompleteKeys` 证明当前页某个账号行无法和
  account-history 核验时，才可只针对本 HTTP 请求携带的账号键（当前页上限 50）通过 `sourceDB` 的
  只读事务复用 `UsagePricing.LoadAccountRowsTx`，再仅对仍不完整的这些键调用
  `LoadAccountRowsFromRetainedEventsTx`。这避免一个无关账号的单个 lag 事件把当前页变成主库长尾扫描。
  可证明完整时立即返回精确 `total_cost`，即使 `sync_status=pending`；无法证明的单个账号才返回金额
  `null`。该单账号 source recovery 使用最多 **1 秒**的子 context；超时/错误时必须保留其他账号已
  验证的 derived 价格，仅把原本不完整的账号保持为 `null`，绝不能让一个慢主库 pricing-tail 查询阻塞
  整个当前页。严禁退化为全量账号、全库 raw history 扫描、HTTP 内写主库或重启/唤醒多个 writer。
- 页面 request/token 仍可由已验证 account-history 返回；价格 source 回退临时失败时，仅把受影响
  core 账号标记 unpriced，不能让整个 `account-history` HTTP 请求卡死，更不能显示零金额或旧 revision
  冒充精确金额。
- 未来合并至少保留以下回归：pricing burst 的 history-first 有界顺序、source target/coverage
  分离、live target `catching_up` 状态、全局 lag 但当前页 coverage 完整时禁止 source pricing 回退、
  不完整单账号 recovery 的 1 秒上界/完整同页价格保留、derived pricing 行缺失时当前页 source 精确金额恢复，以及 full package 的
  store/monitoring/http status 验证。
