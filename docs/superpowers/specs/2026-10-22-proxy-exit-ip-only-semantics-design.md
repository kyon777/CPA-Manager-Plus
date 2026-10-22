# 代理异常仅以出口 IP 直连结果为准：设计

- 日期：2026-10-22
- 状态：已确认设计，待规格审阅
- 范围：仅 CPA Manager Plus（CPAM）
- 不触碰：CPA Core、`cli-proxy-api`、账号 Token、账号调度、优先级、计费、额度采集、窗口用量与历史请求统计

## 1. 背景与根因

当前“代理异常”混入了两类完全不同的证据：

1. 账号巡检请求 `chatgpt.com` / CPA 用量接口发生超时、`502 request failed` 等后，CPAM 以字符串规则推断代理链路异常；
2. 替换代理后，CPAM 又以账号用量接口是否返回 2xx 判断“复检成功”。

两条路径都会把上游拥塞、账号失效、401、配额、CPA 请求积压或浏览器侧历史结果误写成代理异常。因此健康代理会遗留在“代理异常”筛选里，替换成功后也可能因为账号仍返回 401 而不能移除。

本设计把“代理异常”收敛为一个唯一且可复现的事实：**CPAM 使用该账号当前 `proxy_url` 访问固定出口 IP Echo 服务，明确证明代理连接失败。**

账号巡检的业务结果继续用于“重新登录、禁用、启用、保留”等账号动作，但永远不再直接决定“代理异常”。

## 2. 已确认语义

| 事件 | 是否进入“代理异常” | 账号巡检动作 | 代理池状态 |
| --- | --- | --- | --- |
| 账号 API 返回 401、429、额度耗尽、Credits、业务 4xx/5xx | 否 | 维持现有动作 | 不改变 |
| 账号 API 超时 / 指定的底层 502 | 否；仅作为出口检测候选 | 维持现有动作 | 等待出口检测结果 |
| 出口 IP 检测取得合法 IP | 否 | 不改变 | 恢复/保持 `normal` |
| 出口 IP 检测明确失败（407、DNS、拒绝、超时、TLS、传输失败、无效代理 URL） | 是 | 不改变 | 标为 `suspect` 并排除分配 |
| 出口 IP 服务自身 429、4xx/5xx、坏响应、取消 | 否 | 不改变 | 不写异常；结果为“无结论” |

“无结论”绝不等价于“代理异常”，也不等价于“代理健康”。它只在替换任务详情中作为人工核查信息显示，不进入代理异常筛选，也不自动排除代理池。

## 3. 方案选择

### 方案 A（采用）：候选触发 + 固定出口 IP 直检

账号巡检仍按现有流程进行。只有带 `proxy_url` 且命中原有“可能存在传输问题”的窄候选条件时，CPAM 再通过该代理请求固定 IP Echo 服务；只有 Echo 明确失败才标为代理异常。

- 优点：不再把业务错误误判为代理问题；对 5,000+ 账号巡检不会无差别增加数千次 IP Echo 请求；保留全部账号巡检与自动化能力。
- 代价：未发生巡检传输候选的账号不会在本轮被主动标记为代理异常；管理员仍可使用现有代理池自检覆盖全部保存代理。

### 方案 B（不采用）：每个巡检账号都先访问 Echo

每次巡检对所有配置代理的账号都做一次出口检查。

- 优点：获得完整代理普查。
- 缺点：高并发、大账号量下会把固定 Echo 服务和代理源变成新瓶颈，拖慢健康巡检；与现有“巡检并发 80”的运行方式不匹配。

### 方案 C（不采用）：只保留手动代理池自检

移除推断，但不在巡检过程中做任何直接验证。

- 优点：实现最小。
- 缺点：健康巡检的“代理异常”筛选将失去自动更新能力，不能满足替换后立即复检移除的需求。

## 4. 后端数据流

### 4.1 直接出口 IP 检测器

复用现有固定目标的 `ExitIPChecker`：

```text
credential 当前 proxy_url
  -> Go http.Transport.Proxy = http.ProxyURL(proxy_url)
  -> GET 固定 CPAM 配置的 IP Echo URL（默认 https://api.ipify.org?format=json）
  -> 限制响应体 4 KiB，解析 {"ip":"IPv4/IPv6"}
  -> healthy / proxy_failed / inconclusive
```

约束保持不变：每条最多两次、每次五秒、受控并发八路；不携带账号 Token，不请求 `chatgpt.com`，不调用 CPA 用量/额度接口，不产生计费或窗口用量数据。

### 4.2 服务端巡检

`codexinspection.Service` 获得一个只含 `Check(context.Context, proxyURL)` 的出口检测接口。

1. 原账号巡检先生成账号健康结果和既有动作；
2. 原先的 `IsSuspectedProxyTransportFailure` 改为只决定“是否值得发起出口检测”的候选判断，不能再设置 `ProxySuspect`、不能再写代理池状态；
3. 候选项由八路受控的出口检测执行；
4. 仅 `proxy_failed`：写入 `CodexInspectionResult.ProxySuspect=true`，并以明确的 `proxy_*` 原因调用 `MarkSuspect`；
5. `healthy`：结果保持非异常；如该代理已有可恢复的旧状态，恢复为 `normal`；
6. `inconclusive`：结果保持非异常，代理池不写失败状态。

这样 401、429、500、502、超时等账号结果本身永远不能进入代理异常；它们最多触发一次独立的出口 IP 验证。

### 4.3 浏览器本地巡检

浏览器不得再根据 `errorKind`、`errorDetail`、HTTP 状态或原始 `proxy_url` 本地推断代理异常，也不得调用旧的 `inspection-signals` 端点上报原始代理 URL。

新增管理员认证保护的、目标定位型接口：

```http
POST /v0/management/proxy-replacements/credential-exit-ip-checks
Content-Type: application/json

{
  "targets": [
    {
      "clientKey": "...",
      "fileName": "...",
      "provider": "codex",
      "runtimeId": "...",
      "authIndex": "...",
      "accountEmail": "..."
    }
  ]
}
```

服务端使用已存在的严格凭证定位器读取**当前**账号文件的 `proxy_url`，随后调用固定出口 IP 检测器。请求不接受任意代理 URL、任意测试 URL、Token、账号 JSON 或写入字段。单批最大 500 个唯一目标，八路受控并发。

返回按 `clientKey` 关联的 `healthy` / `proxy_failed` / `inconclusive` / `credential_not_found`，可含出口 IP、耗时和有限原因码，但不回显代理 URL。前端仅在 `proxy_failed` 时设置本地 `proxySuspect=true`，并记录 `proxyVerificationSource: "exit_ip"`。没有这个证据来源的旧浏览器缓存一律不再当作异常。

### 4.4 代理替换后的复检

代理写入成功后，替换任务不再调用 CPA/Codex 用量接口，也不再根据 2xx、401 或额度响应判断代理可用性。

```text
写入新 proxy_url 成功
  -> 用严格 target 重新读取该账号当前 proxy_url
  -> 固定出口 IP Echo 复检
  -> healthy:       recheck_succeeded，清除该账号原巡检的 proxy_suspect，恢复新代理池状态
  -> proxy_failed:  recheck_failed，标记新代理 suspect，账号保留在代理异常
  -> inconclusive:  recheck_inconclusive，清除旧代理对应的账号异常，不隔离新代理，任务详情显示“出口检测无结论”
```

`recheck_inconclusive` 是新增持久任务状态，并在任务汇总中单列计数。它避免把“Echo 站无结论”伪装为成功或失败，同时保证旧代理的异常证据不会错误地黏在已经换过代理的账号上。

前端完成替换任务后立即以任务的直接检测结果协调当前巡检视图：

- `healthy` 与 `inconclusive`：从“代理异常”筛选移除；
- `proxy_failed`：保留；
- 凭证定位失败或写入失败：不伪造代理结果，保留任务错误供人工核查。

## 5. 历史脏状态清理

旧版本没有保存“异常证据来源”，因此需要一次性、可重复安全的数据库修正：

1. 清除 `codex_inspection_results.proxy_suspect=1` 的旧推断值；这些值仅由旧账号错误推断路径写入；
2. 将 `proxy_pool_state` 的 `pending_verification` 恢复为 `normal`；
3. 对 `proxy_pool_state.status='suspect'`，仅恢复其 `last_failure_reason` 属于旧推断原因集合（`transport_timeout`、`transport_502_request_failed`、`transport_unknown`）的条目；保留已有直接出口检测原因（`proxy_auth_407`、`proxy_connect_*`、`proxy_dns_failed`、`proxy_tls_failed`、`proxy_transport_failed`、`proxy_url_invalid`）的真正异常状态；
4. 用独立的 CPAM feature-migration 标记保证上述清理只执行一次；不修改 `usage_events`、计费汇总、额度快照或任何认证 JSON。

历史浏览器本地巡检缓存没有 `proxyVerificationSource: "exit_ip"` 时，前端在读取时将其旧 `proxySuspect` 归零，不向服务器写回旧推断。

## 6. 前端交互与文案

### 6.1 巡检结果

- “代理异常”筛选只统计拥有直接 `exit_ip` 失败证据的账号；
- 账号错误的原有显示、重新登录/禁用/启用/保留动作不变；
- 移除“推断代理异常 / 疑似异常”作为该筛选的状态来源；
- 如果出口检测无结论，不在筛选中计数，也不显示为红色代理异常。

### 6.2 更换代理 URL 弹窗

每行映射改为稳定的四列：

```text
账号 | 原代理 URL | 新代理 URL | 出口 IP 复检状态
```

复制映射格式：

```text
账号 | 原代理 URL -> 新代理 URL | healthy / proxy_failed / inconclusive
```

原、新 URL 仅在已有管理员授权的任务详情和复制动作中展示；错误消息、日志、数据库代理状态表仍只存指纹和有限原因码。

状态文案：

- `healthy`：出口检测成功（可附出口 IP / 耗时）；
- `proxy_failed`：代理无法连接出口检测服务（显示有限原因码）；
- `inconclusive`：出口检测无结论，未判定代理异常；
- `credential_not_found`：当前凭证已不存在或身份不再唯一，未进行检测。

中文、英文、繁体中文、俄文 locale 同步补齐，避免回退键名或乱码。

## 7. 安全、性能与兼容性

- 新增端点只接受严格的凭证定位字段；绝不让浏览器指定原始代理 URL 或任意外部 URL；
- 所有出口检查继续使用固定、配置验证过的 Echo URL；
- 对本地巡检候选和替换任务均采用八路并发，避免同一时间对上千代理发起无界连接；
- 不改 CPA Core、`cli-proxy-api`、Docker 网络、OpenResty、现有反代或证书；
- 不访问账号 Token，不触发额度刷新、计费、历史请求写入、优先级降级或账号自动更新；
- 现有代理池手动“出口 IP 自检 / 恢复使用”功能继续有效，且其 `suspect` 语义与新规则一致。

## 8. 测试与验收

### Go 单元与集成测试

1. 账号 401、429、配额、Credits、500、502、超时不会直接置 `ProxySuspect`；
2. 候选触发后：Echo `healthy` 不进入代理异常；Echo `proxy_failed` 才进入；Echo 429/5xx/坏 JSON/取消为 `inconclusive` 且不进入；
3. 固定 Echo URL、两次/五秒边界、八路并发、原因码与 URL 脱敏回归；
4. 凭证 Exit-IP 端点拒绝原始 URL、未知字段、重复/过量 target、非管理员调用；只对当前严格匹配凭证读取 `proxy_url`；
5. 替换后 `healthy`、`proxy_failed`、`inconclusive`、凭证不存在四种任务状态与代理池/巡检结果的精确协调；
6. 一次性历史清理仅修改代理推断状态，重复启动不重复重置新的直接异常，且 `usage_events` 与计费表字节级不变。

### 前端测试

1. 本地巡检不能用 HTTP/错误文本推断代理异常；仅接收服务端 `exit_ip` 结果；
2. 旧 localStorage 的无来源 `proxySuspect` 不显示；
3. 替换后 healthy/inconclusive 立即从代理异常筛选移除，proxy_failed 保留；
4. 弹窗显示原代理、新代理和复检状态，复制文本保持一行一账号；
5. 四个 locale 的新增键存在，批量替换、现有代理池自检、筛选和分页回归通过。

### 部署验收

- 仅构建/部署 CPAM；绝不修改、重启或构建 CPA Core / `cli-proxy-api`；
- 本地和服务器部署前后核验 CPA Core 容器与 `cpa.service` 身份不变；
- 不迁移或覆盖服务器 `/home/cpam/data/*`、`usage.sqlite`、`data.key`、`config/cpam.env`、`secrets/*`；
- 上游只读；最终只推送私有 `origin/lo/custom`。
