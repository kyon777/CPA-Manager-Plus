# CPAM 凭证清单：全量目录 + 当前可见页附加数据

日期：2026-10-16
状态：已确认，待实现

## 目标

恢复凭证列表的全量清单语义：浏览器一次读取 CPA 的完整 `/v0/management/auth-files?include_proxy_url=1` 响应，在前端完成筛选、排序和分页；`note` 与 `proxy_url` 同一响应到达。列表主请求与 CPAM→CPA 桥接超时统一调整到 60 秒。

这次只改 CPAM Web/Manager；不触碰 CPA Core、`cli-proxy-api`、`usage_events`、计费聚合、巡检调度或优先级 worker。

## 已确认的运行时问题

分页实验期间，浏览器在首次列表加载后为约 300 行发出了大量附加请求：`account-history`、`account-window-usage` 和 `credential-runtime-metadata`。其中后两者的客户端中止（499）很多，CPU 被 CPAM 与 CPA 共同拉高，最终用户看到了 30 秒超时。主清单请求本身并非 30 秒瓶颈。

## 最终行为

1. `useAuthFilesData.loadFiles()` 是唯一的清单初始化/刷新路径，并使用既有 `listWithProxyProjection()`，即 CPA `include_proxy_url=1` 响应。
2. `note` 和 `proxy_url` 直接来自全量清单，正常列表不为代理信息下载单账号原始 JSON。
3. 浏览器仍用全量 `rows` 做全局搜索、筛选、排序、标签总数与健康卡片统计；分页只决定可见 50 行。
4. “最近请求”全局轻量索引继续保留，以维持跨全库的最近请求排序。
5. 历史请求、窗口用量、恢复任务只对当前可见页请求；页面/筛选变化会终止已过时的请求。已成功读取过的当前会话历史结果保留在内存，返回页面不重复拉取，显式刷新或连接变更才失效。
6. 恢复状态不再调用会下载账号 JSON 的 `credential-runtime-metadata`。改为一次轻量的 `token-recovery/query` 批量查 SQLite；仍只轮询 `queued/running` 任务。
7. 全局健康卡片由完整 `rows` 本地计算，恢复上游全量清单的真实全局语义。
8. 不添加 `server_paged` / `legacy_fallback` 调试标记（用户明确暂缓）。

## 安全与兼容边界

- 不记录或暴露 token、管理密钥或认证 JSON。
- CPA 返回的代理投影仅用于 UI 和代理筛选，不进入计费数据路径。
- 60 秒仅扩大管理面板读取预算；不影响业务请求、下游调度或计费。
- 旧的 Manager 分页投影调用从正常清单路径删除，避免“分页初始化 + legacy full load”双重读取。
