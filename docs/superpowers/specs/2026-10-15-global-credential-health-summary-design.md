# CPAM 全局凭证健康摘要设计

日期：2026-10-15  
范围：CPA Manager Plus（CPAM）

## 目标

在保留服务端分页列表速度的前提下，让仪表盘卡片反映**全量凭证**，而不是只反映当前 50 条页面。导入 100 个新凭证后，即使当前列表页仍是旧的已启用凭证，`待确认` 卡片也应在库存同步完成后显示这 100 个新凭证。

## 方案

CPAM Manager Server 继续从 CPA Core 分页读取并在内存中保存脱敏库存快照。快照完成后，服务端基于该快照和 Manager SQLite 中已有的只读运行证据生成一个轻量 `global_metrics` 投影；列表接口仍只返回当前页凭证 JSON。浏览器不再为全量凭证下载原始 JSON，也不参与全局计数。

数据流：

```text
CPA /v0/management/auth-files?page=...&include_proxy_url=1
        -> CPAM inventory snapshot (redacted, cached)
        -> read-only evidence projection (inspection/recovery/action/quota/request)
        -> cached global_metrics
        -> current page + ~200-byte summary
```

## API 契约

`GET /usage-service/credential-list` 增加：

```json
{
  "global_metrics": {
    "total": 426,
    "enabled": 326,
    "available": 202,
    "needs_attention": 4,
    "quota_risk": 9,
    "disabled": 100,
    "unconfirmed": 111,
    "needs_inspection_action": 0
  },
  "global_metrics_status": "ready",
  "global_metrics_generated_at_ms": 179... 
}
```

状态：

- `warming`：第一页已返回，后台仍在收集剩余库存；不得把当前页数字伪装成全局数字。
- `ready`：库存完整且摘要成功生成；卡片使用全局数字。
- `stale`：沿用上一次完整摘要，同时列表已经开始刷新。
- `unavailable`：摘要证据暂时不可读；列表仍可用，卡片显示同步提示并回退当前页估算。

`global_metrics` 始终针对完整库存，不受当前搜索、Provider、启用状态筛选影响。筛选和分页只影响 `files`、`total`、`has_more`。

## 分类规则

每个凭证只计入一个主类别，优先级如下：

1. `disabled`：CPA 库存明确标记禁用。
2. `needs_attention`：最近请求为认证/诊断失败、存在待处理操作，或存在恢复任务（排除已完成的成功任务）。
3. `quota_risk`：最近持久化 Header 有额度耗尽/低额度证据，或有活动额度冷却。
4. `available`：存在最近成功请求、健康巡检结果或有效额度快照。
5. `unconfirmed`：启用凭证没有足够的持久化证据，典型是刚导入尚未巡检的新号。

`enabled = total - disabled`。`available + needs_attention + quota_risk + unconfirmed = enabled` 是不变量；如果证据读取失败，摘要标记 `unavailable`，不写入猜测性数据。

证据匹配只使用文件名、auth index、Provider、邮箱/账号快照和 account id 等非敏感身份；不读取或返回 Token、Header 原文、代理认证密码或计费明细。

## 缓存和失效

- 摘要按完整库存快照指纹缓存，短 TTL 复用。
- CPA 库存变化、上传/删除/状态变更调用现有列表失效器；下一次完整快照会重新计算。
- Manager SQLite 证据读取只读，不写 usage、quota、billing 表，不触碰 CPA 调度。
- 首次 warming 期间前端保留已有摘要（若有），否则显示“全局摘要同步中”；同步完成后由现有轮询自然刷新。

## 兼容性与安全边界

- 保持旧构造函数和旧响应字段兼容；没有证据读取器时响应为 `unavailable`。
- 旧 CPA Core 不支持分页时仍沿用已有兼容解析。
- 不改变请求排序、代理筛选、Token Acquisition、自动重新授权、502/503/429 优先级策略或计费数据。
- 不修改、重启、构建或部署 CPA Core / `cli-proxy-api`。

## 验证

覆盖：完整/分页库存的全局计数、刚导入账号进入 `unconfirmed`、禁用和证据优先级、摘要缓存/失效、warming/unavailable 状态，以及前端只在 `ready` 时采用全局卡片。
