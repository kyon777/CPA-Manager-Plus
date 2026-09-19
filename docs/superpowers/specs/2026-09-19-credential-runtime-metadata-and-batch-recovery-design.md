# 凭证运行时元数据与本页批量恢复设计

## 目标

在“凭证列表”中持续显示每个账号 JSON 内的 `proxy_url`（兼容 `proxyUrl`、`proxy-url`），并显示 Codex TokenAcquisition 恢复任务的真实状态与脱敏失败原因。

同时新增“**一键更新本页需重登凭证**”按钮：把当前筛选、排序、分页结果中可恢复的 Codex `需重登` 凭证一次性排入既有的服务器端人工恢复队列，避免逐个打开重登弹窗。

浏览器不得下载完整 CPA 原始 JSON、`refresh_token`、`access_token`、`id_token` 或外部 TokenAcquisition API 密钥。

## 已确认的运行时事实

- CPA Core `GET /v0/management/auth-files` 当前不返回 `proxy_url` / `proxyUrl` / `proxy-url`；因此现有 `AccountRow.proxyUrl` 在刷新后为空，前端不能稳定显示代理。
- CPA Core 已支持按物理文件名下载原始 JSON：`GET /v0/management/auth-files/download?name=...`。
- Manager Server 已有 Token Recovery SQLite 任务状态、任务去重、服务器端 CPA JSON 下载/写回与 TokenAcquisition 调用能力。
- 恢复的允许写入字段仍限定为 `access_token`、`refresh_token`、`id_token` 以及非空的新 `chatgpt_account_id`；代理、备注、优先级、headers 和其余原始字段不会由本功能改写。

## 设计选择

### 方案 A（采用）：Manager Server 最小元数据 API + 批量恢复 API

1. Manager Server 代表已认证面板向 CPA Core 下载当前页目标的原始 JSON，只在进程内解析代理字段。
2. Manager Server 只返回 `proxyUrl`、恢复任务状态、已脱敏的错误码/错误原因；不返回原始 JSON 或 Token。
3. 前端只对当前页请求元数据，切换分页、筛选或账号列表刷新后重新加载。
4. 前端仅在存在 queued/running 恢复任务时轮询轻量的批量任务状态接口；轮询不再下载 CPA JSON。
5. 一键按钮只针对当前页中“Codex + 非 runtimeOnly + 当前健康状态为 `reauth` + 可生成邮箱/auth_index 定位器”的账号。正常可用账号不被无谓重取 Token。

优点：长期稳定、不会将敏感 JSON 送入浏览器、请求量受当前页限制、批量恢复复用已经验证过的服务器端任务去重/文件锁/写回校验链。

### 方案 B（不采用）：前端逐个下载 JSON 后解析代理

实现快，但浏览器会取得完整 Token JSON，且一页 N 个账号会产生 N 个直接 CPA 下载请求。安全边界和可维护性均较差。

### 方案 C（不采用）：修改 CPA Core `/auth-files` 列表响应

API 形态最直接，但需要改动和独立部署 CPA Core，不符合本次仅在 Manager 中完成的目标。

## API 合同

所有新增端点均通过既有 `AuthorizePanel` 鉴权，并沿用 Manager Server 的 CORS 包装。

### 1. `POST /v0/management/credential-runtime-metadata`

请求上限：64 KiB、最多 100 个目标。严格允许字段，拒绝额外字段。

```json
{
  "targets": [
    {
      "clientKey": "浏览器本地 selectionKey",
      "fileName": "physical-file.json",
      "authIndex": "可选",
      "accountEmail": "可选邮箱",
      "provider": "codex"
    }
  ]
}
```

`clientKey` 仅用于把响应映射回当前页面行；服务器绝不将它作为 CPA 文件定位或授权依据。

响应：

```json
{
  "items": [
    {
      "clientKey": "...",
      "proxyUrl": "http://user:pass@host:port",
      "recoveryTask": {
        "status": "auto_running",
        "mode": "auto"
      }
    }
  ]
}
```

- `proxyUrl` 只从目标记录的 `proxy_url`、`proxyUrl`、`proxy-url` 中读取，保留原始非空字符串用于管理员显示。
- JSON 既支持单对象也支持数组；数组优先精确匹配 `auth_index`，无 `auth_index` 时邮箱必须唯一。定位不唯一、文件缺失或 JSON 无效时，该条返回不含 `proxyUrl` 的受控项目错误；不会中断同页其它账号。
- 若 `provider == codex`，响应同时附带当前 `TokenRecoveryTask`；其它 Provider 的 `recoveryTask` 为 `null`。
- 服务端按物理文件名去重下载，同一文件只下载一次；最多 4 个不同文件并发；不记录原始 JSON、Token 或完整代理到日志。

### 2. `POST /v0/management/token-recovery/query`

请求使用与上述相同的 Codex 目标集合；返回每个 `clientKey` 对应的 `TokenRecoveryTask | null`。

前端只在页面中至少存在 `auto_queued`、`auto_running`、`manual_queued` 或 `manual_running` 时每 2 秒轮询它。该接口只查询 SQLite，不读取 CPA JSON。

### 3. `POST /v0/management/token-recovery/manual/batch`

请求：

```json
{
  "targets": [
    {
      "clientKey": "...",
      "fileName": "physical-file.json",
      "authIndex": "可选",
      "accountEmail": "email@example.com",
      "provider": "codex"
    }
  ]
}
```

- 每项调用现有 `TokenRecoveryService.RequestManual`；同一待执行/运行任务返回原任务，不会重复发外部请求。
- 终态任务（含上次自动失败）会被显式转成 `manual_queued`，符合“人工重试才再次获取”的现有语义。
- 返回按 `clientKey` 对齐的成功任务或受控错误；单账号无效不阻断同页其它账号。
- 按钮无候选账号时禁用，并显示数量；点击后直接排队、展示“已提交 N 个，跳过 M 个”的通知。不会自动把已可用或非 Codex 账号加入队列。

## 前端展示与行为

### 凭证列表（表格布局）

身份区域保持原来的文件名与备注；在 `备注:` 右侧增加常驻代理文本：

```text
备注: 2026-09-19    代理 URL: http://host:port
自动更新中
```

- 有代理时显示 `代理 URL:`；无代理时不留下空白占位。
- 代理来自运行时元数据，绝不依赖不完整的 `/auth-files` 列表字段。
- 恢复状态以紧凑状态标签显示：
  - `auto_queued` / `auto_running`：`自动更新中`
  - `manual_queued` / `manual_running`：`手动更新中`
  - `succeeded`：`凭证已更新`
  - 失败：`更新失败：<安全错误原因>`；若安全原因为空则回退到错误码。
- 状态标签在列表、卡片布局中一致显示；“更新失败”可打开既有 Codex 重新授权弹窗进行单账号人工处理。

### 本页批量操作

在凭证列表的工具栏加入：

```text
一键更新本页需重登凭证 (N)
```

`N` 为当前 `pageRows` 中符合资格的 Codex `reauth` 数量，随筛选、排序、分页实时变化。按钮提交后页面立即以批量响应更新状态，再由轻量轮询推进为成功或失败。

## 数据流

```text
凭证页当前 pageRows
  ├─ POST credential-runtime-metadata
  │    └─ Manager Server: CPA Core download -> 内存解析 proxy -> SQLite 查询恢复任务
  │         └─ 前端：备注旁显示代理 + 恢复状态
  │
  └─ 点击“本页需重登凭证”
       └─ POST token-recovery/manual/batch
            └─ 既有 TokenRecoveryService 队列
                 └─ TokenAcquisition -> CPA JSON 合并/上传/二次验证
                      └─ 前端 POST token-recovery/query 轮询到终态
```

## 错误、并发与一致性

- 元数据读取永远不写 CPA；同一物理文件下载失败仅影响该文件相关行。
- 批量入口不绕过既有 `AuthFileMutationCoordinator`、任务去重或 Token 恢复的邮箱校验。
- Browser API 超时、Manager 未配置、CPA 不可用或单个目标定位失败均显示受控错误，不泄露 JSON、Token、Authorization 或外部 API key。
- 页面卸载、分页或连接指纹变更时取消旧请求，并丢弃过期响应，避免旧页面数据覆盖新页面。
- 恢复成功后复用现有凭证刷新/证据失效流程，避免旧的 401/需重登证据重新覆盖新凭证状态。

## 测试与验收

1. Manager API：单对象与数组 JSON 均能只返回目标的代理 URL；未知字段、超限 targets、歧义定位和无鉴权请求被拒绝。
2. Manager API：同一物理文件多个目标只下载一次；一个条目解析失败不影响同批另一个条目。
3. Manager API：Codex 条目返回任务状态，非 Codex 条目不查询/不暴露恢复任务。
4. Token recovery batch：自动失败条目可转为 `manual_queued`；运行中条目去重；非法或非 Codex 目标只产生该项受控错误。
5. 前端：刷新与分页后 `代理 URL:` 仍显示；无代理不渲染空白标签。
6. 前端：每种恢复状态显示正确中文标签和错误原因；只有 pending 状态触发 2 秒任务轮询。
7. 前端：批量按钮只发送当前页 `reauth` Codex 目标，响应后更新行状态与成功/跳过反馈。
8. `go test` 相关服务/控制器、Vitest 相关 hook/page/API 测试、`tsc --noEmit`、Vite production build 全部通过。