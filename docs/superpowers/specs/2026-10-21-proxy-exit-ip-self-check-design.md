# 代理出口 IP 自检设计

- 日期：2026-10-21
- 状态：已确认设计，等待规格审阅
- 范围：仅 CPA Manager Plus（CPAM）
- 不触碰：CPA Core、`cli-proxy-api`、账号 Token、计费、额度采集与调度逻辑

## 1. 目标

现有“疑似异常代理”由单个账号巡检的 `request_error`、超时或特定 `502 request failed` 推断得出。它不是对代理链路的直接验证，因此高并发巡检、上游短暂拥塞或 CPAM 请求积压会产生误判。

本功能改为两层状态：

1. 巡检命中现有推断规则时，只记录为**待验证代理**，不立即从代理分配池排除；
2. 管理员在“筛选未使用的代理 URL”弹窗中执行出口 IP 自检。只有自检确认代理链路失败，才进入真正的“异常代理”状态并从自动分配中排除；自检拿到合法出口 IP 则自动恢复为正常。

检测仅经待测代理请求固定的出口 IP Echo 服务；不访问 `chatgpt.com`、不带账号 Token、不调用 CPA 用量/额度接口。

## 2. 已确认决策

| 项目 | 决策 |
| --- | --- |
| 检测目标 | 固定 JSON IP Echo 地址，默认 `https://api.ipify.org?format=json`；可由 CPAM 环境变量覆盖为同格式的自有出口 IP 服务 |
| 成功条件 | 收到 HTTP 2xx，响应 JSON 的 `ip` 字段可被解析为合法 IPv4 或 IPv6 |
| 自检并发 | 8 路受控并发 |
| 单代理尝试 | 最多 2 次，每次独立 5 秒超时 |
| 初始巡检超时 | 仅进入 `pending_verification`，不排除代理池 |
| 自检成功 | 状态更新为 `normal`，从待验证/异常列表移除，并返回出口 IP 与耗时 |
| 自检明确失败 | 状态更新为 `suspect`，保留在异常列表并从自动分配排除 |
| 检测站异常 | 站点 4xx/5xx、响应格式错误或自身限流均为 `inconclusive`，不把代理判死，也不覆盖已有状态 |
| 人工恢复 | 保留现有“恢复使用”能力；它仅改变代理池资格，不进行网络测试 |

## 3. 非目标

- 不改 CPA Core、`cli-proxy-api`、账号 JSON、Token、优先级、启停状态；
- 不主动刷新额度、Credits、窗口用量、历史统计或计费；
- 不自动更换账号代理、不自动触发账号复检；
- 不把代理 URL 明文写入日志、SQLite 代理状态表或任务摘要；
- 不把 IP Echo 的业务 HTTP 状态误认为账号或代理认证状态。

## 4. 数据与状态模型

现有 `proxy_pool_state` 继续只按规范化 `proxy_url` 的 SHA-256 指纹保存状态，不保存代理认证信息。

状态扩展为：

```text
normal                正常，可参与分配
pending_verification  巡检有传输失败证据，尚未通过出口 IP 自检；仍可参与分配
suspect               出口 IP 自检确认链路失败；从自动分配排除
```

旧数据中的 `normal`、`suspect` 不迁移、不重写。历史 `suspect` 可被新的自检批量验证：成功则恢复 `normal`，明确失败则保留 `suspect`，检测站不可判定时保持原状态。

## 5. 服务端协议与执行流

新增管理员认证保护的端点：

```http
POST /v0/management/proxy-replacements/proxy-pool/verify-exit-ip
Content-Type: application/json

{
  "fingerprints": ["sha256-of-normalized-proxy-url", "..."]
}
```

请求只携带指纹。CPAM 从已保存的 Manager Server 代理列表中按指纹解析 URL；没有匹配保存项的指纹返回 `not_saved`，绝不接受浏览器提交的任意原始 URL 让服务端拨号。

对于每条代理：

```text
fingerprint
  -> 从保存代理池安全解析 proxy_url
  -> Go http.Transport.Proxy = http.ProxyURL(proxyURL)
  -> GET 固定 IP Echo URL
  -> 限制响应体 4 KiB，解析 {"ip":"..."}
  -> 记录仅返回给已认证 UI 的 exitIp / latencyMs / outcome
  -> 按结果原子更新代理池状态
```

自检结果：

| outcome | 状态影响 | 典型原因 |
| --- | --- | --- |
| `healthy` | 改为 `normal` | 成功取得合法出口 IP |
| `proxy_failed` | 改为 `suspect` | 407、代理连接拒绝、代理连接超时、DNS、TLS 失败 |
| `inconclusive` | 不改已有状态 | IP Echo 服务 4xx/5xx/429、非预期响应、服务端自身不可用 |
| `not_saved` | 不改状态 | 指纹不在已保存代理列表 |

错误归类必须保留区分：`proxy_auth_407`、`proxy_connect_timeout`、`proxy_connect_refused`、`proxy_dns_failed`、`proxy_tls_failed`、`ip_echo_unavailable`。不得返回或记录含认证信息的完整代理 URL。

## 6. 巡检信号变化

现有 `inspection-signals` 的分类边界维持不变：仅带代理的 `request_error` 超时或规定的底层 `502 request failed` 可被记录；401、429、额度、Credits 和普通上游业务错误仍不影响代理状态。

变化仅为：

```text
旧：推断命中 -> suspect -> 排除代理池
新：推断命中 -> pending_verification -> 仍可参与代理池 -> 等待出口 IP 自检
```

这样巡检失败仍可被人工追踪，但单次高并发波动不会立即抽干代理池。

## 7. 前端交互

“筛选未使用的代理 URL”弹窗保留当前保存、筛选、复制结果和“恢复使用”操作，并增加：

- `自检全部待验证/异常代理 (N)`：一次请求当前弹窗中保存列表可匹配的待验证与异常代理；
- 每行 `自检`：仅验证该代理；
- 执行期间显示 `已检查 / 正常 / 异常 / 不可判定` 进度，禁用重复自检；
- 完成后立刻刷新代理池状态：健康代理从异常区域移除，异常代理保留并展示具体失败类型；
- 本次结果行展示出口 IP 和耗时；浏览器刷新后状态继续由 Manager Server 决定，出口 IP 展示仅作为本次检测反馈。

文案需要明确区分：

```text
待验证：账号巡检曾超时，尚未证明代理失效
异常：出口 IP 自检确认代理链路失败
检测服务不可判定：不代表代理异常
```

## 8. 安全、性能与一致性

- IP Echo 目标为固定配置值，不接受浏览器指定检测目标；
- 代理 URL 只从服务器已保存代理列表读取；状态表只保留指纹；
- 8 路并发、每条最多 10 秒总测试窗口，避免因上千代理测试造成 CPAM 或代理源无界洪峰；
- 调用不经过 CPA 管理 API，因此不触发账号用量、计费、额度、Token 获取或调度；
- 所有新端点继续使用既有 Manager Server 管理员认证；
- 自检只改变 CPAM 的代理池资格状态，不会写入任何账号文件。

## 9. 测试与验收

### 后端

- 代理 URL 规范化与指纹匹配；
- IP Echo 成功：解析 IPv4/IPv6，恢复 `normal`；
- 407、连接拒绝、连接超时、DNS、TLS：标为 `suspect` 且错误分类正确；
- Echo 4xx/5xx/429/坏 JSON：`inconclusive`，状态不变；
- 原 `suspect`、新 `pending_verification` 和 `normal` 状态的更新边界；
- 八路并发上限与两次重试上限；
- 无保存代理 URL 的指纹不能触发外连；
- 管理端认证、请求大小和 URL 脱敏回归。

### 前端

- 弹窗正确统计待验证与确认异常代理；
- 批量与单行自检请求只提交指纹；
- 执行中防重复点击、完成后状态即时刷新；
- 健康结果显示出口 IP/耗时，失败与不可判定文案区分明确；
- 原筛选、保存、复制、恢复使用功能保持可用。

### 部署验收

- 不构建、不重启、不修改 CPA Core 或 `cli-proxy-api`；
- CPAM 部署前后核验本地 CPA Core 容器与服务器 `cpa.service` 身份未变；
- 上游只读，最终仅允许推送私有 `origin/lo/custom`。
