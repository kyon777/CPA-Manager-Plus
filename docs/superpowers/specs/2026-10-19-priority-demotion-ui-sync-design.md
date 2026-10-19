# 降优先级前端增量同步设计

## 目标

当 CPAM 的持久化 502/503/限定 429/限定 500 降优先级任务完成后，凭证列表在不重新读取 CPA 全量凭证、不触发额度刷新、不读取用量历史的前提下，快速把当前已显示条目的 `priority` 更新为 CPA 已写入的目标值。

## 已确认的运行事实

- 后端持久化队列已把符合条件的错误写入 `server_error_priority_demotion_jobs`，并以 `completed_reason = demoted` 结束成功任务。
- 当前瓶颈不在 8 个降级 worker 或 CPA 写入：请求完成后的任务落库与写入通常在约 2 秒内完成。
- 前端的被动刷新只读 Header 快照、巡检摘要和历史 revision；它不会重新读取 `auth-files`，因此列表中 `priority` 仍是上一次凭证清单的旧快照。

## 设计

### 只读增量接口

新增受现有 Panel Bearer 鉴权保护的接口：

```text
GET /v0/management/server-error-priority-demotions/updates
    ?after_completed_at_ms=<int64>
    &after_id=<int64>
    &limit=<1..200>
```

它只读取 CPAM 的 `usage.sqlite` 队列表，返回以下最小字段：

```json
{
  "items": [
    {
      "id": 42,
      "fileName": "account.json",
      "authIndex": "7",
      "provider": "codex",
      "priorityBefore": 27004,
      "priorityTarget": 27003,
      "completedAtMs": 1760000000000
    }
  ],
  "nextCursor": { "completedAtMs": 1760000000000, "id": 42 }
}
```

查询只匹配 `state = completed`、`completed_reason = demoted`、双 priority 非空，且严格位于二元游标 `(completed_at_ms, id)` 后；按 `(completed_at_ms ASC, id ASC)` 排序。二元游标消除同一毫秒内多条任务造成的漏读或重复。

接口绝不返回事件原文、认证 JSON、令牌、代理 URL、管理密钥、账户快照或计费数据。SQLite 新增索引 `(state, completed_reason, completed_at_ms, id)`，让每秒轮询只扫描目标终态。

### 前端轮询与安全合并

凭证页可见、浏览器标签页可见、管理端已连接且当前列表已加载时，以 1 秒间隔调用该只读接口。请求保持单飞；页面切换、页面隐藏、连接切换或卸载时会中止请求并停止轮询。

前端仅对当前已显示列表做内存替换：

1. 用 `fileName + normalized authIndex + provider` 定位单个凭证。
2. 只有本地 `priority === priorityBefore` 时才改为 `priorityTarget`。
3. 若用户或其他操作已改过优先级，则忽略该条增量，绝不覆盖新值。
4. 未出现在当前列表中的项目只推进游标，不触发全量加载。

整个路径不触发 `loadFiles()`、额度/窗口用量请求、请求历史请求、巡检、CPA 探测或计费写入。

### 启动边界

轮询在当前凭证页完成首个 inventory 加载后启动，以当前客户端时间减去短回看窗口建立游标，覆盖列表请求与轮询建立之间刚完成的任务。若一个项目已经不是 `priorityBefore`，守卫会忽略它。

## 非目标

- 不改变现有错误判定、全局优先级比较、持久化队列 worker 数、重试或 CPA 写入逻辑。
- 不改变 CPA Core、`cli-proxy-api`、请求调度、额度、Credits、计费、历史聚合或巡检逻辑。
- 不对不在当前可见列表的帐号做自动刷新；下一次普通凭证清单加载仍以 CPA 为权威来源。

## 验证标准

1. 仓储查询按二元游标稳定分页，只返回成功 `demoted` 任务。
2. HTTP 路由在 CPA 管理代理之前命中，并要求现有 Panel 鉴权。
3. 前端 API 使用 Manager Server 基址和 Panel Bearer Key。
4. 前端合并只修改匹配且仍保持 `priorityBefore` 的行；人工更改和无关行不受影响。
5. 默认页面行为不会发起全量 `auth-files`、额度、历史或运行时元数据请求。
