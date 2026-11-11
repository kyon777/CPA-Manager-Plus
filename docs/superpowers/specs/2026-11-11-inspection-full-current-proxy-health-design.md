# 全量当前代理出口健康复检设计

**状态：** 已确认，待实现  
**日期：** 2026-11-11  
**范围：** CPA Manager Plus（CPAM）私有二改；不触碰 CPA Core / `cli-proxy-api`。

## 1. 问题与目标

现有 Codex 巡检只在账户探测失败、且错误文本符合少数网络候选规则时，才调用
`credential-exit-ip-checks`。因此旧账号即使当前 `proxy_url` 已失效，只要 Codex
用量接口仍返回 HTTP 200，就不会出现在“代理异常”中。

目标是在一次成功的巡检完成后，对**所有具有当前 `proxy_url` 的巡检结果**做独立
出口-IP健康复检：启用、禁用、历史账号和 HTTP 200 均包括在内。该复检只判断代理
链路，不改变账户原有巡检分类、用量、计费、额度、Token 恢复或优先级。

## 2. 非目标与不变量

- 不要求代理出现在“保存代理 URL 列表”或代理池中。
- 不读取每个账号一个 CPA 接口；单个 Manager 批次只取一次完整的当前凭证代理投影。
- 浏览器请求与响应、localStorage、日志、巡检结果中均不暴露原始 `proxy_url`。
- 不发 Codex API 调用、不检查额度、不写入凭证文件，因此不影响计费或客户端 API。
- 不更改 CPA Core、`cli-proxy-api`、主计费 SQLite、独立派生库或其调度。

## 3. 架构与数据流

### 3.1 浏览器侧

1. 巡检原有的账户探测先照常执行并生成结果。
2. 使用已有的一次 `listWithProxyProjection` 当前凭证快照，按严格身份解析每个结果的
   当前凭证；没有当前代理的结果跳过。
3. 对每个当前代理计算既有的规范化 SHA-256 指纹。若结果携带同指纹的既有出口复检
   证据，则无需再次复检；新巡检结果没有证据，必定进入复检。
4. 以指纹合并相同代理；每组仅发送一个代表账号的严格定位字段（文件名、provider、
   authIndex/runtime id、账户快照/账户 id），绝不发送 URL。每次端点请求最多 500 个
   代表定位字段。
5. 收到代表结果后，按组扇出到全部绑定账户，更新安全的复检证据和 `proxySuspect`。
   因此一个失效代理可立即使全部绑定账号进入既有“代理异常”筛选。

### 3.2 Manager 服务端

`POST /v0/management/proxy-replacements/credential-exit-ip-checks` 保持既有请求结构和
500 项上限。服务端在一个批次内：

1. 校验和去重严格定位字段，解析当前 CPA setup。
2. 通过 `FetchWithProxyURLs`（如果支持）或 `Fetch` 一次性取得完整当前凭证清单。
3. 从这个内存快照严格匹配每个定位字段，拒绝缺失、歧义或身份不一致的项；这些结果为
   `inconclusive`，并且没有原始 URL。
4. 对匹配条目的规范化当前代理 URL 分组，每个唯一 URL 一次固定出口-IP检查。
5. 使用 40 路仅用于 credential exit-IP jobs 的 worker pool 执行。代理池手动检查保留
   既有独立并发限制。将同组结果扇出回各 `client_key`。

出口-IP检查继续使用现有 `https://api.ipify.org?format=json`、两次尝试、每次 5 秒。

## 4. 结果语义

| 服务器 outcome | 浏览器 `proxySuspect` | “代理异常” |
| --- | --- | --- |
| `proxy_failed` | `true` | 纳入 |
| `healthy` | `false` | 不纳入 |
| `inconclusive` | `false` | 不纳入 |
| `not_saved` / 当前无代理 / 严格匹配失败 | `false` | 不纳入 |

`proxy_failed` 包含代理 407、连接拒绝、DNS/TLS/连接/传输失败和超时。出口服务本身返回
异常、非预期响应、取消或不能验证时均是 `inconclusive`，不能误杀账号。

## 5. 本地持久化与失效

每条巡检结果仅允许保存：

```ts
proxyVerificationSource: 'exit_ip'
proxyVerificationOutcome: 'healthy' | 'proxy_failed' | 'inconclusive' | 'not_saved'
proxyVerificationReason: string // 已净化且无 URL
proxyVerificationFingerprint: string // SHA-256
proxyVerificationCheckedAtMs: number
```

展示历史结果时，只有当前代理存在且其指纹匹配存储证据时，才信任旧的
`proxySuspect`。当前代理变更、无代理、或旧存储缺少指纹时，旧标记不作为异常展示，
并在下一轮复检中刷新。新巡检运行始终重新验证。

## 6. 性能与安全边界

- 500 个代表账号的 HTTP 请求由浏览器分批顺序调用；单批内服务器最多 40 个唯一代理
  检查。重复代理的实际网络量为 1。
- 每个 Manager 批次只有一次完整凭证投影读取；无需 500/5000 次 CPA 请求。
- 服务器结果只含 `client_key`、outcome 和安全 reason；浏览器 localStorage 不含代理 URL。
- 代理健康状态仅用于巡检筛选；不触碰请求路由、会话粘性、额度刷新、派生库与会计事实。

## 7. 验收与回归

1. HTTP 200 的禁用旧账号仍会发送一个严格 locator 并在其代理 407 时出现“代理异常”。
2. 两个账号共用当前代理时，Manager 的 checker 只运行一次，两个结果都获得同一 outcome。
3. Manager 批次只抓取一次凭证代理投影，严格 identity 不匹配不允许探测。
4. 40 个不同代理可并行启动；代理池检查并发不被改变。
5. `healthy`、`inconclusive`、空代理、旧 fingerprint 都不会制造假异常。
6. 结果可安全保存/恢复；存储文本中不出现原始代理 URL。
7. Web 定向测试、Go 定向测试、type-check 和构建通过；部署必须另行明确批准。
