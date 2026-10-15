# CPAM 服务端分页凭证列表与代理投影设计

## 目标

在不修改 CPA Core 请求路由、不改变 API 计费、用量采集、价格计算、优先级策略或 Token Acquisition 行为的前提下，使 CPAM 在约 3,000 个凭证、下游约 500 并发请求时，仍能快速打开、翻页、搜索和按“最近请求”排序凭证列表。

普通列表首屏必须把 `note` 与 `proxy_url` 一起返回；不得因为展示代理而为当前页下载原始认证 JSON。

## 已确认的现状

1. 当前 Web `authFilesApi.list()` 未传 `page` / `page_size`，浏览器会取得完整凭证数组，再在前端构造行、筛选、排序并 `slice` 当前页。
2. CPA 已支持分页。实测参数必须使用 snake_case：`page` 与 `page_size`；`pageSize` 和 `limit` 不生效。
3. 当前 `/v0/management/auth-files` 响应直接含 `note`，但不含 `proxy_url`；所以备注首屏显示。
4. 当前代理 URL 来自 `POST /v0/management/credential-runtime-metadata`。Manager Server 对每个物理文件下载原始认证 JSON，再解析目标记录的代理，默认最大下载并发为 4；因此代理会晚于备注出现。
5. 当前 CPAM 的 auth-files 原始响应缓存只拦截**无 query string**的请求。带 `page`、`page_size` 或 `include_proxy_url` 的请求会直通 CPA；这不会返回错误页，但也不会获得分页缓存。
6. 已存在 auth-files 缓存、最近请求轻量索引、服务端批量 JSON 作业等二改，必须保留而非回退。

## CPA → CPAM 的新接口契约

CPA Core 正在提供以下查询参数：

```text
GET /v0/management/auth-files?page=1&page_size=50&include_proxy_url=1
```

CPAM 的任何普通列表读取都必须传这三个参数，默认每页 50 条；不得再从列表代码发起无分页的 `/auth-files` 请求。

当 `include_proxy_url=1` 时，CPA 应在每个逻辑凭证的 `files[]` 项中返回：

```json
{
  "name": "account.json",
  "auth_index": "...",
  "email": "user@example.com",
  "note": "2026-10-15",
  "proxy_url": "http://user:pass@host:port"
}
```

兼容与准确性要求：

- `proxy_url` 必须是当前逻辑凭证记录的值；同一物理 JSON 存在多个账号时，按 `auth_index` / `email` 精确投影，绝不读取 sibling 的代理。
- 支持原始字段别名 `proxy_url`、`proxyUrl`、`proxy-url`。
- 推荐 `include_proxy_url=1` 时每项都带 `proxy_url`：无代理返回空字符串。这样 CPAM 能确定 CPA 已支持该投影，而不是把“所有账号没有代理”误判为旧 Core 不支持。
- 若 CPA 只能省略空字段，则必须同时返回顶层 capability，例如 `proxy_url_included: true`，或等效响应头。
- 不增加 `access_token`、`refresh_token`、完整认证 JSON 或 headers 的输出。
- CPA 内部应从已加载的逻辑认证记录投影该字段；不得为了清单请求逐个再次下载/解析物理 JSON。

## 不可变边界：计费与请求链路

以下组件绝不改变行为、SQL 口径或网络通道：

- `collector`、RESP 消费、`usage_events` 写入、小时聚合、价格/成本计算；
- CPA Core / `cli-proxy-api` 的模型请求、账号选择、转发与数据卷；
- Token Acquisition、自动恢复、502/503/精确 429 降优先级的业务规则；
- 实际模型请求的 headers、token 统计和费用记录。

新列表路径只允许：

- 向 CPA 的**管理端点**读取分页凭证投影；
- 对 CPAM SQLite 做只读查询，或写入与用量表隔离的凭证摘要/代理投影缓存；
- 返回脱敏的列表投影。

不得写入 token、完整认证 JSON 或任何模型请求数据。每项列表回归测试必须证明：不会调用模型/转发 API、不会插入或更新 `usage_events`、不会改变价格/成本聚合。

## 新请求链

```text
浏览器
  -> CPAM credential-list（页码 / 搜索 / 筛选 / 排序）
      -> CPAM 凭证摘要索引、最近请求索引、额度/恢复任务摘要
      -> 需要同步页时：
         CPA /v0/management/auth-files?page=N&page_size=50&include_proxy_url=1
      -> 返回当前页 50 条完整列表投影
```

浏览器可以兼容访问 CPAM 的 `/v0/management/auth-files?page=N&page_size=50&include_proxy_url=1`；但 Accounts 页面最终使用 CPAM 聚合列表层，以保持全局“最近请求”排序、服务端筛选、统计卡片、代理筛选和稳定批量操作语义。

也就是说，CPAM → CPA 的清单请求严格使用用户指定的分页 URL；浏览器 → CPAM 不必为了全局排序而被迫放弃聚合端点。

## CPAM 配套改造

### 1. 真分页与分页缓存

- `authFilesApi.list` 增加分页参数，默认发出：

  ```text
  /v0/management/auth-files?page=1&page_size=50&include_proxy_url=1
  ```

- CPAM proxy 的 auth-files 缓存升级为**分页变体缓存**。缓存键必须包含：

  ```text
  setup 安全摘要 + page + page_size + include_proxy_url
  ```

  不得只按 setup 缓存，否则第 1 页会错误地复用于第 2 页。
- 所有 auth-files 上传、删除、启停、优先级、字段编辑、OAuth 回写和自动 Token 更新完成后，必须失效该 setup 的全部分页变体及摘要索引。
- 每页最多 50（或受服务端硬限制的安全上限）；禁止 UI 再请求“全量 page_size=3000”。

### 2. 普通列表禁止自动 runtime metadata 下载

- `proxy_url` 以 CPA 分页清单为权威来源，直接进入 AccountRow 和 CPAM 摘要索引。
- 普通凭证页加载、翻页、排序、筛选时，**不再调用** `credential-runtime-metadata` 来读取代理。
- `credential-runtime-metadata` 仅保留给：
  - 打开单账号详情时的精确原始文件校验；
  - CPA 不支持 `include_proxy_url` 的短期兼容降级；
  - 明确的人工诊断操作。
- Core capability 已确认后，列表场景不得静默回退为每页下载原始 JSON；若代理投影异常，显示明确错误并保留最后已知安全缓存值。
- 自动恢复一次、恢复失败原因、`account_banned` 和“删除当前页已封禁账号”的判断改从 CPAM 本地 Token Recovery / 巡检摘要批量读取，不再绑定 runtime metadata 文件下载。

### 3. 代理筛选

- 代理筛选读取 CPAM 凭证摘要索引中所有**已启用**账号的 `proxy_url`，不受当前页影响。
- 不再用浏览器把全部账号切成 80 条一批、串行调用 runtime metadata。
- 缓存不完整时，弹窗显示同步状态；未知代理不得被误判为“未使用”。
- 所有比较继续保留现有规则：trim、去除末尾 `/`、完整 URL 比较、保存在 Manager Server。

### 4. 最近请求、额度和恢复状态

- 最近请求全局排序在 CPAM 服务端完成，复用轻量 recent index；刷新后仍是全局正确顺序。
- 当前页才批量 join 最近请求显示、用量窗口、Header/额度/Credits 快照、巡检摘要及 Token Recovery task 摘要。
- 详情抽屉才请求完整历史、事件列表、完整窗口计算或原始文件运行时校验。
- Credits 与 5 小时 / 7 天 / 月额度既有合并规则不变；只改变查询边界和传输量。

## 首次加载与代理 URL 的最终行为

| 场景 | `note` | `proxy_url` | 原始 JSON 下载 |
|---|---|---|---|
| CPA 已支持 `include_proxy_url=1` | 与列表同响应 | 与列表同响应 | 否 |
| CPAM 代理投影热缓存 | 与列表同响应 | 与列表同响应 | 否 |
| CPA 正在灰度/不支持 capability | 与列表同响应 | 明确兼容状态或最后已知值 | 仅受控诊断/兼容路径 |
| 打开账号详情 | 同上 | 可重新精确校验 | 仅该账号/物理文件 |

因此生产目标是：普通列表首屏中备注和代理 URL 同时显示，而不是先渲染备注、再异步补代理。

## 搜索、筛选和批量行为

- 搜索使用 250–400 ms debounce，条件变化重置至第 1 页。
- Provider、套餐、状态、额度、风险等筛选及统计卡片由服务端索引返回；不再让浏览器持有 3,000 条原始行。
- “当前页更新”“删除当前页已封禁账号”“复制当前页账号”继续只作用于服务端返回的当前页，语义不变。
- 如果未来增加“全部筛选结果批量操作”，必须传服务器端 query snapshot / generation 并二次确认；禁止把前端行数组当作全量目标。
- 批量粘贴 JSON 成功后，仅失效当前查询页、facets 与受影响索引；不再全量 `loadFiles()`。

## 错误与降级

- 凭证摘要索引尚未建好：默认第一页可直接走 CPA 的真实分页请求；复杂全局排序/筛选显式显示同步状态，不能返回不完整的伪全局结果。
- SQLite 的轻量摘要查询失败：基础列表仍显示；最近请求/额度显示已有快照或 `-`，不能阻塞列表。
- CPA 管理读取超时：保留最后一次完整安全索引并显示同步年龄；禁止退回浏览器全量下载。
- 代理 capability 缺失或单项投影异常：不把该代理判断为未使用，也不自动触发全页 raw JSON 扫描。

## 验收与测试

### 正确性

1. 3,000 条凭证时，首屏浏览器响应最多包含 `page_size` 条摘要，不含 token / 完整认证 JSON。
2. 请求参数精确为 `page`、`page_size`、`include_proxy_url=1`；每个页码的缓存变体彼此隔离。
3. `note` 与 proxy URL 在同一列表响应渲染；普通列表不触发页面级 `credential-runtime-metadata`。
4. 最近请求按全局最新时间排序，刷新后仍保持正确。
5. 搜索、筛选、total、facets、分页与当前页动作均正确。
6. 代理筛选以所有启用凭证为范围；未知项绝不判为未使用。
7. 上传、OAuth、自动恢复、删除、priority 修改后，对应页、代理与索引立即失效/更新。

### 计费隔离

1. 访问列表、翻页、搜索、刷新、代理筛选不会调用模型请求接口。
2. 每项操作前后 `usage_events` 行数、成本汇总、collector 状态保持不变。
3. 列表服务只允许访问管理 `/v0/management/auth-files` 系列；不得进入 Collector、Pricing 或 Usage Writer。
4. 下游高并发期间，inventory 同步队列与管理读取并发受限，collector 的 RESP 配置与吞吐基线不变。

### 性能

1. 3,000 条测试清单下，浏览器初始化只解码当前 50 条；不会构造/排序 3,000 个 React 行。
2. 翻页仅请求当前页摘要与当前页轻量本地 join，不下载原始认证 JSON。
3. 正常打开列表不产生全量 history、全量 window usage、全量 runtime metadata 或全量原始 JSON 下载。
4. 代理筛选热缓存不触发原始 JSON 下载；冷状态不能阻塞正常列表，也不能误判未使用代理。

## 实施范围

仅修改 CPAM：`apps/manager-server`、`apps/web`、测试及二改记忆文档。不会修改或重启 `cli-proxy-api`、CPA Core、CPA 数据卷或其配置。
