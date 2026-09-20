# 凭证代理 URL 缺失筛选设计

## 目标

在凭证列表中增加“代理 IP 筛选”入口。用户输入多行代理 URL 后，Manager Server 持久化这些值；筛选时只检查当前 Manager Server 已加载的全部凭证中“已启用”账号的 `proxy_url`，返回输入列表里没有被任何已启用账号使用的代理 URL，并支持逐行复制。

## 行为约定

- 粘贴认证 JSON 弹窗默认类型为“CPA 认证 JSON”。
- 代理值按 `trim` 后去掉末尾 `/` 规范化；空行丢弃；重复值只保留一次。
- 比较使用规范化后的完整字符串，不改变大小写或代理凭据内容。
- 只把 `disabled !== true` 的凭证视为已启用；没有代理的凭证不占用任何输入值。
- 筛选范围是 Manager Server 返回的全部凭证，不受当前分页限制。
- 保存发生在 Manager Server 的 SQLite settings 表中，不使用浏览器 `localStorage`。
- 读写复用面板的 `Authorization: Bearer <managementKey>` 认证；读取允许面板认证，写入要求管理员认证。

## 数据流

1. Accounts 页面打开筛选弹窗。
2. 前端向 Manager Server `GET /usage-service/proxy-filter` 读取已保存的规范化列表。
3. 用户编辑多行输入，点击保存时 `PUT /usage-service/proxy-filter`；服务端再次规范化并原子替换 settings 记录。
4. 点击筛选，前端从当前 `files` 全集提取已启用账号的 `proxy_url`，计算差集。
5. 结果在弹窗中显示，并通过 Clipboard API 按一行一个值复制。

## 失败处理

- Manager Server 不可用时保留输入内容，显示错误，不产生浏览器端持久化副本。
- 凭证列表仍在加载时禁止筛选，避免把未加载数据误判为缺失。
- 复制失败时显示通知，不清空结果。

## 兼容性

- 新 settings key 缺失时返回空列表，不影响现有数据和旧部署。
- 新路由不改变 CPA Core 数据或现有认证文件。
