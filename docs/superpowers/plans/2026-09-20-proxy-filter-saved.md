# 代理 URL 缺失筛选实现计划

## 1. 先写可失败测试

- 为代理 URL 规范化和差集算法添加 Vitest 单元测试：空行、尾部斜杠、重复值、禁用账号、全量数据。
- 为 CPA 粘贴弹窗添加测试，确认初始选择值为 `cpa`，重置后仍为 `cpa`。
- 为 Manager Server settings repository 添加保存/读取 round-trip 测试。
- 为 HTTP handler 添加 GET/PUT 鉴权和规范化响应测试。

## 2. 后端持久化与接口

- 新增 `ProxyFilterSettings` 模型和 setting key。
- 扩展 settings repository/store 的保存、读取方法。
- 新增 `/usage-service/proxy-filter` GET/PUT handler；GET 使用面板鉴权，PUT 使用管理员鉴权。
- 在 router 注册路由，并加入规范化、去重、空值限制。

## 3. 前端纯逻辑与 API

- 新增代理 URL规范化/差集纯函数。
- 在 usageService 增加读取/保存 API 类型和方法。
- 增加 i18n 文案。

## 4. 前端交互

- Accounts 工具栏加入“代理 IP 筛选”按钮。
- 新增弹窗：多行输入、保存、筛选、缺失列表、复制、状态/错误反馈。
- 使用 `files` 全集中的已启用凭证计算差集；加载未完成时禁用筛选。
- 粘贴认证 JSON 默认改为 CPA 类型，并同步 reset。

## 5. 验证与部署

- 运行前端相关 Vitest、TypeScript/ESLint 构建和 Go `gofmt`/`go test ./...`。
- 检查 git diff，确认没有改动 CPA 上游或数据卷。
- 提交到 `lo/custom` 并只推送 `origin`（个人 fork），不推送 upstream。
