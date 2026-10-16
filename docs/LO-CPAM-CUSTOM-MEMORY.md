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

- 只对 HTTP `502`、`503` 自动降低优先级；不是所有 50x。
- HTTP `429` 只有同时满足响应体 `{"detail":"Rate limit exceeded"}` 才进入限流降优先级路径，普通 429 不触发。
- 降优先级下限为 0。
- 降级时排除当前账号，读取其他账号最高优先级：若最高值低于当前值，目标设为“其他最高值 - 1”；否则当前值减 1；最终 clamp 到 0，避免高优先级账号降一级后仍持续被选中。
- 优先级调整、失败原因和恢复状态必须可追踪，不能只显示笼统的“更新失败”。

### Codex 401 自动恢复与人工恢复

- 只针对“已启用且巡检判定需要重新登录”的账号。
- 开关打开后，每个账号最多自动尝试一次；无论成功或失败都写入一次性标记，后续不自动重复。
- 列表显示“已自动更新一次”或可读失败原因，方便人工核查。
- 自动失败后保留人工重试按钮；按钮操作不应破坏一次性自动尝试语义。
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
- 文案必须与实际策略一致：只处理新入库的 HTTP `502`、`503`，或响应体精确为 `{"detail":"Rate limit exceeded"}` 的 HTTP `429`；比较同 Provider 的已启用账号优先级，按 peer-rebalance 规则降级，最低为 0。

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
