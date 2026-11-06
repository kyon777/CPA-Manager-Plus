# 出口 IP → 账号绑定监控 CPAM 插件页设计

**日期：** 2026-11-05  
**状态：** 已获 LO 确认，待实现

## 目标

将现有 `per-auth-rate-limit` 插件已经提供的只读诊断接口整合进 CPA Manager Plus 的“Codex 全局限流”插件页。管理员不再需要手动访问原始接口，即可查看当前出口 IP 到账号的绑定和最近的绑定事件。

现有 CPA 接口保持不变：

```text
GET /v0/management/plugins/per-auth-rate-limit/egress-diagnostics?limit=100
```

该接口仅在已启用的 `per-auth-rate-limit` v0.8.2 或更高版本中可用。

## 范围与非目标

范围：

- 在现有“Codex 全局限流”嵌入式插件页面增加第二个标签页：`出口绑定监控`。
- 在 CPAM 的既有限流插件 `postMessage` 桥接中增加一个固定、只读的 `diagnostics` 操作。
- 显示当前绑定表和最近 100 条事件；支持手动刷新。
- 监控标签可见时每 5 秒刷新一次；切出标签、iframe 卸载或页面不可见时停止轮询。
- 将更新后的插件页面作为新的 Linux/amd64 `.so` 工件，与 CPAM 前端作为两个独立、手动激活的发布包。

非目标：

- 不改变 CPA 选号、会话粘性、账号/IP 并发、重试、冷却、计费或认证文件行为。
- 不新增通用 URL 转发、通用管理 API 代理或 iframe 内的管理密钥暴露。
- 不保存诊断历史到 CPAM 数据库；数据继续由 CPA 插件内存中的固定 1024 条环形缓冲区提供。
- 不自动重启 CPA 或 CPAM。

## 架构与数据流

```text
管理员打开 CPAM → Codex 全局限流 → 出口绑定监控标签
  → 插件 iframe 向父页面发送严格的 diagnostics postMessage
  → CPAM 仅验证当前 iframe window + 精确 CPA origin + 固定 pluginID/protocol
  → CPAM 固定调用 /v0/management/plugins/per-auth-rate-limit/egress-diagnostics?limit=100
  → CPAM 校验并投影允许字段
  → postMessage 返回 iframe
  → iframe 渲染当前绑定及最近事件
```

CPAM 直接通过已保存的 CPA 地址和管理密钥调用 CPA；它不会通过公网回环，也不会将密钥传入 iframe。浏览器对 iframe 的请求只有 CPAM 的受控桥接响应。

## 桥接协议

沿用现有协议：`cpamp.per-auth-rate-limit/v1`，且固定插件 ID 为 `per-auth-rate-limit`。

新增请求：

```json
{
  "protocol": "cpamp.per-auth-rate-limit/v1",
  "pluginID": "per-auth-rate-limit",
  "requestID": "diagnostics_123",
  "operation": "diagnostics",
  "limit": 100
}
```

约束：

- `limit` 可省略；默认 100；仅接受整数 1–1024。
- 不接受 `url`、`path`、`method`、请求头或任意参数字段来决定上游调用。
- 仅当前活跃 iframe window、其精确资源 origin 和合法 requestID 可触发调用。
- 诊断请求不进入现有保存队列，也不改变任何配置。

成功响应使用同一 `result` envelope，并包含 `diagnostics`；失败响应仅返回固定中文错误文本，不回传底层管理密钥、代理 URL 或原始异常。

## 数据投影与保密边界

CPA 已经只返回脱敏字段。CPAM 仍将再次验证并投影响应：

当前绑定：

- `egress_host`
- `owner_auth_id`
- `hold_kind`
- `hold_until`
- `updated_at`
- `in_flight`
- `release_after_in_flight`

最近事件：

- `at`
- `event`
- `reason`
- `egress_host`
- `auth_id`
- `hold_kind`
- `hold_until`

禁止透传：代理用户名/密码、端口、完整 proxy URL、access token、refresh token、id token、管理密钥、请求 body。

CPAM 将限制返回量：最多 1024 条事件，绑定记录及字符串字段也有合理上界。格式异常或超过边界的上游字段被丢弃，而不是原样送入 iframe。

## 页面行为

### 标签 1：限流设置

保持现有完整设置页面与保存行为不变。

### 标签 2：出口绑定监控

显示：

1. `当前绑定`
   - 出口 IP
   - 当前绑定账号
   - 当前 in-flight
   - 保持原因
   - 保持截止时间/剩余时间
   - 最后更新时间
   - 延迟释放标记
2. `最近事件`
   - 时间
   - 事件
   - 出口 IP
   - 账号
   - 原因
   - 保持类型/截止时间
3. 工具栏
   - `立即刷新`
   - 最后刷新时间
   - 加载中/错误/空状态

时间使用浏览器本地时区显示。账号 ID 仅显示 CPA 所给的逻辑账号标识；不解码或读取认证 JSON。

标签切换到监控时立即加载一次；仅在该标签处于活动状态且文档可见时，以 5 秒节奏刷新。一个刷新尚未返回时不启动第二个并行刷新。

## 错误处理

- CPAM 桥接超时、CPA 管理鉴权失败、插件尚未升级、接口 404 或数据格式不合法：页面保留最后一次成功数据并显示可读错误；不会导致设置标签失效。
- 老版本插件没有 `diagnostics` 操作：旧 CPAM 可继续正常加载/保存设置；新页面显示需要更新限流插件的错误。
- 老版本 CPAM 未实现 `diagnostics`：新插件页面的设置标签仍可用；监控标签显示桥接未响应/需要更新 CPAM 的错误。

## 测试与验证

CPAM：

- 桥接仅接受受信 iframe + 精确 origin 的 `diagnostics` 请求。
- 非法 limit、伪造 operation、任意路径/URL 字段不产生上游调用。
- 正常诊断响应被投影后返回；敏感或格式异常字段不返回 iframe。
- CPA 请求固定为 `/plugins/per-auth-rate-limit/egress-diagnostics`，查询参数仅为合法 `limit`。
- 既有 `load`、`save` 与保存串行化回归通过。

插件：

- 新页面发出固定 `diagnostics` 请求并正确消费 `diagnostics` result。
- 初次打开监控标签加载一次；活动标签文档可见时每 5 秒轮询；离开标签、卸载或不可见时停止。
- 手动刷新不会和正在进行的刷新并发。
- 原有页面设置加载、保存和主题桥接回归通过。

发布：

- 构建 Linux/amd64 `per-auth-rate-limit` 新工件并验证 SHA-256。
- 构建 CPAM Web/Manager Server 镜像并验证对应前端资源。
- 生成两个独立、回滚可用的手动激活脚本；不自动中断运行中的 CPA/CPAM。