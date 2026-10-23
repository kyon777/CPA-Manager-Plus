# 批量复制授权文件 JSON Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** 让凭证列表中已选的逻辑账号一次性导出为可直接粘贴到另一台 CPA 的顶层 JSON 数组，并在共享源文件、局部失败和剪贴板失败时保持严格边界与可核查反馈。

**Architecture:** 浏览器只向 CPAM Manager Server 提交无密钥的稳定身份字段。Manager Server 使用一次 CPA 文件清单验证每个目标，再按物理源文件去重、以 8 路受控并发读取原始 JSON、精确抽取每个已选对象、按用户选择顺序返回 records 数组。浏览器只序列化 records 数组并写入剪贴板；此操作不刷新额度、不读取历史、不改变文件、不改变调度。

**Tech Stack:** Go 1.24、net/http、现有 cpaauthfiles CPA 客户端、React 19、TypeScript、Vitest、现有 Modal、DropdownMenu、copyToClipboard、downloadBlob 工具。

---

## 范围与不可变约束

- 仅改 CPAM 的 apps/manager-server 和 apps/web；绝不改 CPA Core、cli-proxy-api、计费、调度、巡检、优先级、自动更新和已有批量下载。
- “复制授权文件数据”放入凭证列表已选行浮动工具栏的“更多”菜单，紧邻原有“批量下载”；现有按钮全部保留。
- 每批最多 300 个逻辑账号；最终可复制 JSON 数组最多 48 MiB；物理源文件读取并发固定最多 8。
- 一个物理 JSON 含多个账号时，只导出被勾选且验证通过的对象；任何身份不确定都不能退化为整份文件导出。
- 原始认证 JSON 只存在于 CPA 响应内存、Manager Server 请求内存、HTTPS 响应和管理员本机剪贴板；不写 SQLite、磁盘、缓存、队列或日志正文。端点所有响应发送 Cache-Control: no-store。
- 正常路径直接复制；只有剪贴板失败时才在内存结果弹窗持有 JSON，供再次复制或浏览器 Blob 下载。局部失败弹窗只保存文件名和固定错误码，不保存 JSON。
- 导出不会调用 quota、history、recent-request、account-window-usage、巡检、自动更新、优先级或 loadFiles。

## 文件结构

| 文件 | 责任 |
| --- | --- |
| apps/manager-server/internal/service/cpaauthfiles/auth_json_export.go | 从一个原始 CPA JSON 源精确抽取一个逻辑账号对象。 |
| apps/manager-server/internal/service/cpaauthfiles/auth_json_export_test.go | 覆盖对象源、数组源、歧义与坏 JSON。 |
| apps/manager-server/internal/service/authfilesexport/service.go | 身份验证、物理文件去重、8 路读取、保序、48 MiB 汇总限制。 |
| apps/manager-server/internal/service/authfilesexport/service_test.go | 覆盖清单一次读取、共享源一次下载、局部失败、并发上限与总量限制。 |
| apps/manager-server/internal/http/controller/authfilesexport/handler.go | 管理员鉴权、白名单解码、no-store、安全错误映射。 |
| apps/manager-server/internal/http/controller/authfilesexport/handler_test.go | 覆盖鉴权、字段拒绝、上限与响应不泄密。 |
| apps/manager-server/internal/app/context.go | 构造导出服务。 |
| apps/manager-server/internal/http/router/router.go | 在通用 CPA 管理反代之前路由新端点。 |
| apps/manager-server/internal/http/router/router_test.go | 证明新端点不会落入 CPA Core 反代。 |
| apps/web/src/services/api/usageService.ts | 导出请求/响应类型与 Manager Server API 调用。 |
| apps/web/src/services/api/usageService.credentialRuntime.test.ts | 证明浏览器只发送身份字段。 |
| apps/web/src/features/accounts/model/authJsonExportTargets.ts | 从已选 AuthFileItem 构造最小身份目标。 |
| apps/web/src/features/accounts/model/authJsonExportTargets.test.ts | 覆盖无 token/raw JSON/proxy 的目标构建。 |
| apps/web/src/features/accounts/components/AuthJsonExportResultModal.tsx | 安全失败详情与仅剪贴板失败时的 JSON 兜底。 |
| apps/web/src/features/accounts/components/AuthJsonExportResultModal.module.scss | 结果弹窗样式。 |
| apps/web/src/features/accounts/components/AuthJsonExportResultModal.test.tsx | 覆盖 JSON 显示边界、重试复制和下载入口。 |
| apps/web/src/features/accounts/AccountsPage.tsx | 菜单项、防重、调用、通知、弹窗状态清理。 |
| apps/web/src/features/accounts/AccountsPage.test.tsx | 覆盖菜单、复制内容、局部失败与不刷新列表。 |
| apps/web/src/i18n/locales/zh-CN.json、en.json、zh-TW.json、ru.json | 菜单、状态、固定错误码文案。 |

### Task 1: 增加原始 JSON 的精确单记录提取器

**Files:**
- Create: apps/manager-server/internal/service/cpaauthfiles/auth_json_export.go
- Create: apps/manager-server/internal/service/cpaauthfiles/auth_json_export_test.go
- Test: apps/manager-server/internal/service/cpaauthfiles/proxy_patch_test.go

- [ ] **Step 1: 先写失败测试，锁定共享数组只提取已选成员。**

~~~go
func TestExtractAuthJSONRecordSelectsOnlyRequestedArrayMember(t *testing.T) {
	raw := []byte("[{\"auth_index\":\"7\",\"email\":\"a@example.com\",\"refresh_token\":\"rt-a\",\"note\":\"keep-a\"},{\"auth_index\":\"8\",\"email\":\"b@example.com\",\"refresh_token\":\"rt-b\",\"note\":\"keep-b\"}]")

	record, err := ExtractAuthJSONRecord(raw, AuthJSONRecordLocator{
		AuthIndex: "8", AccountEmail: "b@example.com",
	})
	if err != nil {
		t.Fatalf("ExtractAuthJSONRecord() error = %v", err)
	}
	want := "{\"auth_index\":\"8\",\"email\":\"b@example.com\",\"refresh_token\":\"rt-b\",\"note\":\"keep-b\"}"
	if string(record) != want {
		t.Fatalf("record = %s, want %s", record, want)
	}
}

func TestExtractAuthJSONRecordRejectsMissingArrayIndexAndInvalidJSON(t *testing.T) {
	if _, err := ExtractAuthJSONRecord(
		[]byte("[{\"auth_index\":\"7\",\"email\":\"a@example.com\"}]"),
		AuthJSONRecordLocator{AccountEmail: "a@example.com"},
	); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("missing array auth index error = %v", err)
	}
	if _, err := ExtractAuthJSONRecord([]byte("{"), AuthJSONRecordLocator{}); !errors.Is(err, ErrAuthJSONInvalid) {
		t.Fatalf("invalid JSON error = %v", err)
	}
}
~~~

- [ ] **Step 2: 运行测试，确认没有现成实现。**

Run: cd apps/manager-server; go test ./internal/service/cpaauthfiles -run 'TestExtractAuthJSONRecord' -count=1

Expected: FAIL，编译器报告 ExtractAuthJSONRecord、AuthJSONRecordLocator 和 ErrAuthJSONInvalid 未定义。

- [ ] **Step 3: 实现原语；对象源由后续 CPA 清单身份验证保护，数组源必须存在唯一 auth_index。**

~~~go
package cpaauthfiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrAuthJSONInvalid = errors.New("auth JSON source is invalid")

type AuthJSONRecordLocator struct {
	AuthIndex    string
	AccountEmail string
}

func ExtractAuthJSONRecord(raw []byte, locator AuthJSONRecordLocator) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%w: source is empty", ErrAuthJSONInvalid)
	}
	switch trimmed[0] {
	case '{':
		var record map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &record); err != nil || record == nil {
			return nil, fmt.Errorf("%w: decode object", ErrAuthJSONInvalid)
		}
		if !matchesObjectLocator(record, locator) {
			return nil, fmt.Errorf("%w: object locator mismatch", ErrIdentityMismatch)
		}
		return append(json.RawMessage(nil), trimmed...), nil
	case '[':
		if strings.TrimSpace(locator.AuthIndex) == "" {
			return nil, fmt.Errorf("%w: auth index is required for an array", ErrIdentityMismatch)
		}
		var records []json.RawMessage
		if err := json.Unmarshal(trimmed, &records); err != nil || records == nil {
			return nil, fmt.Errorf("%w: decode array", ErrAuthJSONInvalid)
		}
		matches := make([]json.RawMessage, 0, 1)
		for _, encoded := range records {
			var record map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &record); err != nil || record == nil {
				return nil, fmt.Errorf("%w: decode array member", ErrAuthJSONInvalid)
			}
			if matchesArrayLocator(record, locator) {
				matches = append(matches, append(json.RawMessage(nil), encoded...))
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("%w: array locator is missing or ambiguous", ErrIdentityMismatch)
		}
		return matches[0], nil
	default:
		return nil, fmt.Errorf("%w: root must be object or array", ErrAuthJSONInvalid)
	}
}

func matchesObjectLocator(record map[string]json.RawMessage, locator AuthJSONRecordLocator) bool {
	if expected := strings.TrimSpace(locator.AuthIndex); expected != "" {
		if actual := strings.TrimSpace(rawString(record["auth_index"])); actual != "" && actual != expected {
			return false
		}
	}
	if expected := normalizeAuthJSONEmail(locator.AccountEmail); expected != "" {
		if actual := normalizeAuthJSONEmail(readAuthJSONEmail(record)); actual != "" && actual != expected {
			return false
		}
	}
	return true
}

func matchesArrayLocator(record map[string]json.RawMessage, locator AuthJSONRecordLocator) bool {
	if strings.TrimSpace(rawString(record["auth_index"])) != strings.TrimSpace(locator.AuthIndex) {
		return false
	}
	expected := normalizeAuthJSONEmail(locator.AccountEmail)
	return expected == "" || normalizeAuthJSONEmail(readAuthJSONEmail(record)) == expected
}

func readAuthJSONEmail(record map[string]json.RawMessage) string {
	for _, key := range []string{"email", "account_email", "accountEmail"} {
		if value := rawString(record[key]); value != "" {
			return value
		}
	}
	for _, key := range []string{"user", "account"} {
		var nested map[string]json.RawMessage
		if json.Unmarshal(record[key], &nested) == nil {
			if value := rawString(nested["email"]); value != "" {
				return value
			}
		}
	}
	return ""
}

func normalizeAuthJSONEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
~~~

Single-object sources do not need a raw auth_index to be exported: there are no sibling records, and Task 2 first verifies the source against CPA inventory. A present conflicting auth_index or e-mail still fails closed.

- [ ] **Step 4: 补充嵌套 user.email、对象邮箱冲突和原有 proxy patch 回归测试。**

~~~go
func TestExtractAuthJSONRecordAcceptsSingleObjectAndGuardsPresentEmail(t *testing.T) {
	raw := []byte("{\"type\":\"codex\",\"user\":{\"email\":\"A@example.com\"},\"refresh_token\":\"rt\",\"proxy_url\":\"http://p\"}")
	record, err := ExtractAuthJSONRecord(raw, AuthJSONRecordLocator{AuthIndex: "runtime-only", AccountEmail: "a@example.com"})
	if err != nil || string(record) != string(raw) {
		t.Fatalf("record=%s err=%v", record, err)
	}
	if _, err := ExtractAuthJSONRecord(raw, AuthJSONRecordLocator{AccountEmail: "other@example.com"}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("mismatched object email error = %v", err)
	}
}
~~~

- [ ] **Step 5: 运行新旧 cpaauthfiles 测试。**

Run: cd apps/manager-server; go test ./internal/service/cpaauthfiles -count=1

Expected: PASS。

- [ ] **Step 6: 提交解析原语。**

~~~bash
git add apps/manager-server/internal/service/cpaauthfiles/auth_json_export.go apps/manager-server/internal/service/cpaauthfiles/auth_json_export_test.go
git commit -m "feat: extract selected auth JSON records safely"
~~~


### Task 2: 实现受控 Manager Server 导出服务

**Files:**
- Create: apps/manager-server/internal/service/authfilesexport/service.go
- Create: apps/manager-server/internal/service/authfilesexport/service_test.go

- [ ] **Step 1: 写失败测试，验证共享文件只下载一次且 records 保持调用方选择顺序。**

~~~go
func TestServiceExportsSelectedMembersInCallerOrderWithOneDownloadPerPhysicalSource(t *testing.T) {
	client := &recordingExportClient{
		files: []cpaauthfiles.File{
			{ID: "runtime-a", Name: "shared.json", AuthIndex: "1", Provider: "codex", AccountSnapshot: "a@example.com", AccountID: "account-a"},
			{ID: "runtime-b", Name: "shared.json", AuthIndex: "2", Provider: "codex", AccountSnapshot: "b@example.com", AccountID: "account-b"},
		},
		downloads: map[string][]byte{
			"shared.json": []byte("[{\"auth_index\":\"1\",\"email\":\"a@example.com\",\"token\":\"a\"},{\"auth_index\":\"2\",\"email\":\"b@example.com\",\"token\":\"b\"}]"),
		},
	}
	service := NewWithOptions(Options{SetupResolver: configuredExportSetup{}, AuthFiles: client})
	result, err := service.Export(context.Background(), []Target{
		{ClientKey: "b", FileName: "shared.json", AuthIndex: "2", AccountEmail: "b@example.com", Provider: "codex", RuntimeID: "runtime-b", AccountID: "account-b", AccountSnapshot: "b@example.com"},
		{ClientKey: "a", FileName: "shared.json", AuthIndex: "1", AccountEmail: "a@example.com", Provider: "codex", RuntimeID: "runtime-a", AccountID: "account-a", AccountSnapshot: "a@example.com"},
	})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if client.fetchCalls != 1 || client.downloadCalls("shared.json") != 1 {
		t.Fatalf("fetch=%d download=%d", client.fetchCalls, client.downloadCalls("shared.json"))
	}
	got, _ := json.Marshal(result.Records)
	want := "[{\"auth_index\":\"2\",\"email\":\"b@example.com\",\"token\":\"b\"},{\"auth_index\":\"1\",\"email\":\"a@example.com\",\"token\":\"a\"}]"
	if string(got) != want {
		t.Fatalf("records=%s want=%s", got, want)
	}
}

func TestServiceKeepsSuccessfulRecordsWhenOneSourceFails(t *testing.T) {
	client := &recordingExportClient{
		files: []cpaauthfiles.File{{ID: "a", Name: "a.json", Provider: "codex"}, {ID: "b", Name: "b.json", Provider: "codex"}},
		downloads: map[string][]byte{"a.json": []byte("{\"email\":\"a@example.com\"}")},
		errs: map[string]error{"b.json": errors.New("upstream unavailable")},
	}
	result, err := NewWithOptions(Options{SetupResolver: configuredExportSetup{}, AuthFiles: client}).Export(context.Background(), []Target{
		{ClientKey: "a", FileName: "a.json", Provider: "codex", RuntimeID: "a"},
		{ClientKey: "b", FileName: "b.json", Provider: "codex", RuntimeID: "b"},
	})
	if err != nil || result.Exported != 1 || len(result.Failed) != 1 || result.Failed[0].Code != "source_unavailable" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
~~~

Use this complete test scaffolding in `service_test.go`; it gives every test helper above an exact contract and keeps raw source bytes out of failure observations:

~~~go
type configuredExportSetup struct{}

func (configuredExportSetup) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return store.Setup{
		CPAUpstreamURL: "http://cpa.test:8317",
		ManagementKey:  "test-management-key",
	}, true, nil
}

type recordingExportClient struct {
	mu                  sync.Mutex
	files               []cpaauthfiles.File
	downloads           map[string][]byte
	errs                map[string]error
	fetchCalls          int
	downloadCallsByName map[string]int
}

func (c *recordingExportClient) Fetch(context.Context, string, string) ([]cpaauthfiles.File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetchCalls++
	return append([]cpaauthfiles.File(nil), c.files...), nil
}

func (c *recordingExportClient) Download(_ context.Context, _ string, _ string, name string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.downloadCallsByName == nil {
		c.downloadCallsByName = map[string]int{}
	}
	c.downloadCallsByName[name]++
	if err := c.errs[name]; err != nil {
		return nil, err
	}
	return append([]byte(nil), c.downloads[name]...), nil
}

func (c *recordingExportClient) downloadCalls(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.downloadCallsByName[name]
}
~~~

The blocking concurrency fake extends `recordingExportClient` with `started chan struct{}`, `release chan struct{}`, `active int`, and `maximum int`. Its Download increments active under the same mutex, sends started, waits on release, then decrements active. The test must start five unique physical source jobs and release only after observing exactly two starts. Failure assertions inspect Result.Failed only; no fake raw record, token, or proxy value may be copied there.

- [ ] **Step 2: 运行测试，确认新服务包尚不存在。**

Run: cd apps/manager-server; go test ./internal/service/authfilesexport -count=1

Expected: FAIL，目录或包 authfilesexport 不存在。

- [ ] **Step 3: 定义服务契约与硬上限。**

~~~go
package authfilesexport

const (
	MaxTargets     = 300
	MaxConcurrency = 8
	MaxExportBytes = 48 * 1024 * 1024
)

var (
	ErrUnavailable         = errors.New("selected auth JSON export is unavailable")
	ErrTooManyTargets      = errors.New("selected auth JSON export accepts at most 300 targets")
	ErrExportLimitExceeded = errors.New("selected auth JSON export exceeds 48 MiB")
)

type SetupResolver interface {
	ResolveSetup(context.Context) (store.Setup, bool, error)
}

type AuthFilesClient interface {
	Fetch(context.Context, string, string) ([]cpaauthfiles.File, error)
	Download(context.Context, string, string, string) ([]byte, error)
}

type Options struct {
	SetupResolver SetupResolver
	AuthFiles     AuthFilesClient
	Concurrency   int
}

type Target struct {
	ClientKey       string
	FileName        string
	AuthIndex       string
	AccountEmail    string
	Provider        string
	RuntimeID       string
	AccountID       string
	AccountSnapshot string
}

type FailedItem struct {
	ClientKey string
	FileName  string
	Code      string
}

type Result struct {
	Requested int
	Exported  int
	Records   []json.RawMessage
	Failed    []FailedItem
}
~~~

In the real source, attach the current lower-camel JSON names to Target, FailedItem, and Result fields: clientKey, fileName, authIndex, accountEmail, provider, runtimeId, accountId, accountSnapshot, requested, exported, records, failed, and code. No struct admits a token field, a raw body field, or a proxy field.

- [ ] **Step 4: 实现 Export 的确定性流程。**

Implement NewWithOptions, Export, normalizeTarget, validTarget, identityFor, classifyIdentityError, classifyExtractError, composeResult, encodedRecordsSize, recordDownloadFailure, recordExtractionOutcome, and sortedPhysicalJobs with exactly this sequence:

1. More than MaxTargets returns an empty Result plus ErrTooManyTargets before CPA access. Empty input returns an empty Result without CPA access.
2. Trim all strings and lower-case provider/e-mail. Invalid or duplicate clientKey creates a per-target invalid_target failure; it carries no raw error message.
3. Resolve stored CPA setup. Unconfigured or failed setup makes every otherwise valid target source_unavailable; return a normal Result because this is a batch-local read failure.
4. Fetch CPA inventory exactly once. A fetch error makes every otherwise valid target source_unavailable.
5. For every candidate call cpaauthfiles.VerifyIdentity using AuthFileName, RuntimeID, AuthIndex, Provider, AccountSnapshot and AccountIDSnapshot. Map ErrAuthFileNotFound to credential_not_found; map every other resolution failure to identity_mismatch.
6. If verified File.Name is empty, mark runtime_only. Otherwise group candidates by lower-cased physical File.Name.
7. Create at most min(concurrency, numberOfGroups) workers. One worker calls Download exactly once per physical group. A not-found download maps to credential_not_found; all other download failures map to source_unavailable for only that group.
8. For every group candidate invoke cpaauthfiles.ExtractAuthJSONRecord(raw, cpaauthfiles.AuthJSONRecordLocator{AuthIndex: target.AuthIndex, AccountEmail: target.AccountEmail}). Map ErrAuthJSONInvalid to invalid_source_json and ErrIdentityMismatch to identity_mismatch.
9. Save every success in an outcome slot at its original target index. After workers finish, append only successful slots from index zero upward. Marshal that final slice once and reject the entire export with an empty Result plus ErrExportLimitExceeded if the byte length exceeds MaxExportBytes.
10. Do not acquire MutationCoordinator locks, mutate CPA, invalidate inventory, create jobs, write a file, write SQLite, or log source bodies. This is an isolated read operation; a concurrent source change results in strict source extraction failure or the current same-identity value, never sibling export.

Use this worker shape; protect indexed outcome writes with one mutex:

~~~go
jobs := make(chan physicalJob)
var workers sync.WaitGroup
for worker := 0; worker < workerCount; worker++ {
	workers.Add(1)
	go func() {
		defer workers.Done()
		for job := range jobs {
			raw, err := s.authFiles.Download(ctx, setup.CPAUpstreamURL, setup.ManagementKey, job.physicalName)
			if err != nil {
				recordDownloadFailure(outcomes, job.indexes, err)
				continue
			}
			for _, candidate := range job.targets {
				record, extractErr := cpaauthfiles.ExtractAuthJSONRecord(raw, cpaauthfiles.AuthJSONRecordLocator{
					AuthIndex: candidate.target.AuthIndex,
					AccountEmail: candidate.target.AccountEmail,
				})
				recordExtractionOutcome(outcomes, candidate.index, candidate.target, record, extractErr)
			}
		}
	}()
}
for _, job := range sortedPhysicalJobs(jobsByPhysicalName) {
	jobs <- job
}
close(jobs)
workers.Wait()
~~~

- [ ] **Step 5: 添加不依赖时序的并发和边界测试。**

~~~go
func TestServiceBoundsPhysicalDownloadsToConfiguredConcurrency(t *testing.T) {
	client := newBlockingExportClient(5)
	service := NewWithOptions(Options{SetupResolver: configuredExportSetup{}, AuthFiles: client, Concurrency: 2})
	done := make(chan Result, 1)
	go func() {
		result, _ := service.Export(context.Background(), client.targets())
		done <- result
	}()
	<-client.started
	<-client.started
	if active, maximum := client.activeCounts(); active != 2 || maximum != 2 {
		t.Fatalf("active=%d maximum=%d", active, maximum)
	}
	close(client.release)
	<-done
}
~~~

Add explicit tests for credential_not_found, identity_mismatch, invalid_source_json, empty physical source name (runtime_only), 301 targets, aggregate output over 48 MiB, and a shared array where one selected item succeeds while another fails identity validation. Every failure assertion must prove the result JSON does not contain the test token or test proxy literal.

- [ ] **Step 6: 运行服务测试。**

Run: cd apps/manager-server; go test ./internal/service/authfilesexport ./internal/service/cpaauthfiles -count=1

Expected: PASS。

- [ ] **Step 7: 提交服务层。**

~~~bash
git add apps/manager-server/internal/service/cpaauthfiles/auth_json_export.go apps/manager-server/internal/service/authfilesexport/service.go apps/manager-server/internal/service/authfilesexport/service_test.go
git commit -m "feat: export selected auth JSON through manager server"
~~~


### Task 3: 加入 HTTP 边界、应用注入与路由优先级

**Files:**
- Create: apps/manager-server/internal/http/controller/authfilesexport/handler.go
- Create: apps/manager-server/internal/http/controller/authfilesexport/handler_test.go
- Modify: apps/manager-server/internal/app/context.go
- Modify: apps/manager-server/internal/http/router/router.go
- Modify: apps/manager-server/internal/http/router/router_test.go

- [ ] **Step 1: 写控制器和路由失败测试。**

~~~go
func TestHandlerRejectsTokenBearingExportPayload(t *testing.T) {
	h := &Handler{App: authorizedTestApp(t), Service: exportServiceStub{}}
	req := httptest.NewRequest(
		http.MethodPost,
		exportSelectedPath,
		strings.NewReader("{\"targets\":[{\"clientKey\":\"a\",\"fileName\":\"a.json\",\"provider\":\"codex\",\"runtimeId\":\"runtime-a\",\"access_token\":\"secret\"}]}"),
	)
	rec := httptest.NewRecorder()
	h.Handle(rec, req)
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestRouterRoutesSelectedAuthExportBeforeCoreManagementProxy(t *testing.T) {
	appCtx := routerTestApp(t)
	appCtx.AuthFileExportService = authfilesexport.NewWithOptions(authfilesexport.Options{
		SetupResolver: configuredSetupResolver{},
		AuthFiles: fakeAuthFilesClient{},
	})
	rec := httptest.NewRecorder()
	router.New(appCtx).ServeHTTP(rec, authorizedRequest(
		http.MethodPost,
		"/v0/management/auth-files/export-selected",
		"{\"targets\":[]}",
	))
	if rec.Code == http.StatusBadGateway {
		t.Fatalf("request reached CPA management proxy")
	}
}
~~~

- [ ] **Step 2: 运行新测试。**

Run: cd apps/manager-server; go test ./internal/http/controller/authfilesexport ./internal/http/router -run 'TestHandlerRejectsTokenBearingExportPayload|TestRouterRoutesSelectedAuthExportBeforeCoreManagementProxy' -count=1

Expected: FAIL，导出控制器、路由或 Context.AuthFileExportService 未定义。

- [ ] **Step 3: 实现控制器的固定路径、no-store 和安全错误映射。**

~~~go
const (
	exportSelectedPath = "/v0/management/auth-files/export-selected"
	maxRequestBytes    = 512 * 1024
)

type exportService interface {
	Export(context.Context, []authfilesexportsvc.Target) (authfilesexportsvc.Result, error)
}

type Handler struct {
	App     *app.Context
	Service exportService
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h == nil || h.App == nil || h.App.AdminAuthService == nil || h.Service == nil {
		response.Error(w, http.StatusServiceUnavailable, errors.New("selected auth JSON export is unavailable"))
		return
	}
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	if strings.TrimRight(r.URL.Path, "/") != exportSelectedPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	targets, err := decodeTargets(r.Body)
	if err != nil {
		response.Error(w, http.StatusBadRequest, errors.New("invalid selected auth JSON export request"))
		return
	}
	result, err := h.Service.Export(r.Context(), targets)
	switch {
	case errors.Is(err, authfilesexportsvc.ErrUnavailable):
		response.Error(w, http.StatusServiceUnavailable, errors.New("selected auth JSON export is unavailable"))
	case errors.Is(err, authfilesexportsvc.ErrTooManyTargets), errors.Is(err, authfilesexportsvc.ErrExportLimitExceeded):
		response.JSON(w, http.StatusRequestEntityTooLarge, map[string]string{"code": "export_limit_exceeded"})
	case err != nil:
		response.Error(w, http.StatusInternalServerError, errors.New("selected auth JSON export failed"))
	default:
		response.JSON(w, http.StatusOK, result)
	}
}
~~~

decodeTargets must use the existing controller pattern: bounded io.ReadAll, one root JSON object, a second decoder call requiring io.EOF, then one strict target object at a time. Its exact permitted target fields are:

~~~go
allowed := map[string]struct{}{
	"clientKey": {}, "fileName": {}, "authIndex": {}, "accountEmail": {},
	"provider": {}, "runtimeId": {}, "accountId": {}, "accountSnapshot": {},
}
~~~

After unmarshal, trim all values and lower-case provider/e-mail. Reject any other field, any duplicate clientKey, blank clientKey/fileName/provider, or a target with neither authIndex nor runtimeId. Never decode into a generic field bag after validation, so raw JSON, token fields and proxy credentials cannot cross the browser-to-Manager boundary.

- [ ] **Step 4: 注入服务并在泛化反代前路由。**

Add AuthFileExportService with concrete type authfilesexportsvc.Service to app.Context. In fromExisting construct it with the same managerConfigService and authFiles client already created for CPAM's other auth-file operations:

~~~go
AuthFileExportService: authfilesexportsvc.NewWithOptions(authfilesexportsvc.Options{
	SetupResolver: managerConfigService,
	AuthFiles:     authFiles,
}),
~~~

In router.New construct the controller using appCtx.AuthFileExportService. Add its handler parameter to rootHandler. Before the generic branch that forwards every path beginning with /v0/management/ to proxyHandler.Management, add:

~~~go
if strings.TrimRight(r.URL.Path, "/") == "/v0/management/auth-files/export-selected" {
	middleware.WithCORS(appCtx.Config, authFileExportHandler.Handle)(w, r)
	return
}
~~~

- [ ] **Step 5: 完成 HTTP 安全测试矩阵。**

Test all of these exact conditions:

~~~text
POST requires panel authorization.
GET returns 405.
Unknown root key, unknown target key, duplicate clientKey, blank required field,
two concatenated JSON documents, and a body larger than 512 KiB return 400.
301 targets return 400 before the service is called.
All response paths have Cache-Control: no-store.
The 48 MiB response is status 413 with exactly {"code":"export_limit_exceeded"}.
Failure responses do not contain a fake token, fake proxy URL, or raw auth JSON.
~~~

- [ ] **Step 6: 运行服务、控制器和路由测试。**

Run: cd apps/manager-server; go test ./internal/service/authfilesexport ./internal/http/controller/authfilesexport ./internal/http/router -count=1

Expected: PASS。

- [ ] **Step 7: 提交 API 边界。**

~~~bash
git add apps/manager-server/internal/http/controller/authfilesexport apps/manager-server/internal/app/context.go apps/manager-server/internal/http/router/router.go apps/manager-server/internal/http/router/router_test.go
git commit -m "feat: add selected auth JSON export endpoint"
~~~


### Task 4: 建立前端无密钥 API 与选择目标构建器

**Files:**
- Modify: apps/web/src/services/api/usageService.ts
- Modify: apps/web/src/services/api/usageService.credentialRuntime.test.ts
- Create: apps/web/src/features/accounts/model/authJsonExportTargets.ts
- Create: apps/web/src/features/accounts/model/authJsonExportTargets.test.ts

- [ ] **Step 1: 写 API 和模型失败测试。**

~~~ts
it('exports selected auth JSON with identity fields only', async () => {
  await usageServiceApi.exportSelectedAuthFiles(BASE, KEY, [{
    clientKey: 'row-a', fileName: 'a.json', authIndex: '7',
    accountEmail: 'a@example.com', provider: 'codex', runtimeId: 'runtime-a',
    accountId: 'account-a', accountSnapshot: 'a@example.com',
  }]);

  expect(axios.post).toHaveBeenCalledWith(
    BASE + '/v0/management/auth-files/export-selected',
    { targets: [{ clientKey: 'row-a', fileName: 'a.json', authIndex: '7', accountEmail: 'a@example.com', provider: 'codex', runtimeId: 'runtime-a', accountId: 'account-a', accountSnapshot: 'a@example.com' }] },
    expect.objectContaining({ timeout: USAGE_SERVICE_TIMEOUT_MS, headers: expect.objectContaining({ Authorization: 'Bearer ' + KEY }) })
  );
});

it('builds an export target without credentials or proxy data', () => {
  const target = buildAuthJsonExportTarget({
    ...makeAuthFile({ name: 'a.json', authIndex: '7', account: 'a@example.com' }),
    access_token: 'must-not-cross',
    refresh_token: 'must-not-cross',
    proxy_url: 'http://must-not-cross',
  } as AuthFileItem);
  expect(target).toEqual(expect.objectContaining({ fileName: 'a.json', provider: 'codex' }));
  expect(JSON.stringify(target)).not.toContain('must-not-cross');
});
~~~

- [ ] **Step 2: 运行新测试。**

Run: npm --workspace apps/web run test -- src/services/api/usageService.credentialRuntime.test.ts src/features/accounts/model/authJsonExportTargets.test.ts

Expected: FAIL，exportSelectedAuthFiles 和 buildAuthJsonExportTarget 未定义。

- [ ] **Step 3: 在 usageService 定义端点契约。**

~~~ts
export interface AuthJsonExportTargetRequest {
  clientKey: string;
  fileName: string;
  authIndex?: string;
  accountEmail?: string;
  provider: string;
  runtimeId?: string;
  accountId?: string;
  accountSnapshot?: string;
}

export interface AuthJsonExportFailureItem {
  clientKey: string;
  fileName: string;
  code:
    | 'credential_not_found'
    | 'identity_mismatch'
    | 'invalid_source_json'
    | 'source_unavailable'
    | 'runtime_only'
    | 'invalid_target'
    | string;
}

export interface AuthJsonExportResponse {
  requested: number;
  exported: number;
  records: Record<string, unknown>[];
  failed: AuthJsonExportFailureItem[];
}
~~~

Add this usageServiceApi member next to deleteAccountBannedAuthFilesBatch:

~~~ts
exportSelectedAuthFiles: async (
  base: string,
  managementKey: string | undefined,
  targets: AuthJsonExportTargetRequest[],
  signal?: AbortSignal
): Promise<AuthJsonExportResponse> => {
  if (__DEMO_SITE__ && isDemoMode()) {
    return {
      requested: targets.length,
      exported: 0,
      records: [],
      failed: targets.map(({ clientKey, fileName }) => ({
        clientKey,
        fileName,
        code: 'demo_mode',
      })),
    };
  }
  return withUsageServiceError(async () => {
    const response = await axios.post<AuthJsonExportResponse>(
      buildUrl(base, '/v0/management/auth-files/export-selected'),
      { targets },
      {
        timeout: USAGE_SERVICE_TIMEOUT_MS,
        headers: authHeaders(managementKey),
        ...optionalAbortSignal(signal),
      }
    );
    return response.data;
  });
},
~~~

- [ ] **Step 4: 创建最小身份目标构建器。**

~~~ts
import type { AuthFileItem } from '@/types';
import type { AuthJsonExportTargetRequest } from '@/services/api/usageService';
import {
  getAuthFilePatchTarget,
  getAuthFileSelectionKey,
} from '@/features/authFiles/model/credentialStatus';

const normalize = (value: unknown) => typeof value === 'string' ? value.trim() : '';
const normalizeProvider = (value: unknown) => normalize(value).toLowerCase();
const normalizeEmail = (value: unknown) => {
  const email = normalize(value).toLowerCase();
  return email.includes('@') ? email : '';
};

export const buildAuthJsonExportTarget = (
  file: AuthFileItem
): AuthJsonExportTargetRequest | null => {
  const patch = getAuthFilePatchTarget(file);
  const clientKey = normalize(getAuthFileSelectionKey(file));
  const fileName = normalize(patch.name);
  const provider = normalizeProvider(patch.provider ?? file.provider ?? file.type);
  const authIndex = normalize(patch.authIndex);
  const runtimeId = normalize(patch.runtimeId);
  if (!clientKey || !fileName || !provider || (!authIndex && !runtimeId)) return null;

  const nestedUser = file.user as { email?: unknown } | undefined;
  const accountSnapshot = normalize(patch.accountSnapshot);
  const accountEmail =
    normalizeEmail(accountSnapshot) ||
    normalizeEmail(file.email) ||
    normalizeEmail(file.account) ||
    normalizeEmail(nestedUser?.email) ||
    normalizeEmail(file['account_email']);
  const accountId = normalize(patch.accountId);

  return {
    clientKey,
    fileName,
    provider,
    ...(authIndex ? { authIndex } : {}),
    ...(accountEmail ? { accountEmail } : {}),
    ...(runtimeId ? { runtimeId } : {}),
    ...(accountId ? { accountId } : {}),
    ...(accountSnapshot ? { accountSnapshot } : {}),
  };
};

export const buildAuthJsonExportTargets = (files: readonly AuthFileItem[]) => {
  const seen = new Set<string>();
  return files.flatMap((file) => {
    const target = buildAuthJsonExportTarget(file);
    if (!target || seen.has(target.clientKey)) return [];
    seen.add(target.clientKey);
    return [target];
  });
};
~~~

- [ ] **Step 5: 运行前端 API 与模型测试。**

Run: npm --workspace apps/web run test -- src/services/api/usageService.credentialRuntime.test.ts src/features/accounts/model/authJsonExportTargets.test.ts

Expected: PASS。

- [ ] **Step 6: 提交前端 API 边界。**

~~~bash
git add apps/web/src/services/api/usageService.ts apps/web/src/services/api/usageService.credentialRuntime.test.ts apps/web/src/features/accounts/model/authJsonExportTargets.ts apps/web/src/features/accounts/model/authJsonExportTargets.test.ts
git commit -m "feat: add client for selected auth JSON export"
~~~


### Task 5: 实现列表菜单、直接复制与受限结果弹窗

**Files:**
- Create: apps/web/src/features/accounts/components/AuthJsonExportResultModal.tsx
- Create: apps/web/src/features/accounts/components/AuthJsonExportResultModal.module.scss
- Create: apps/web/src/features/accounts/components/AuthJsonExportResultModal.test.tsx
- Modify: apps/web/src/features/accounts/AccountsPage.tsx
- Modify: apps/web/src/features/accounts/AccountsPage.test.tsx
- Modify: apps/web/src/i18n/locales/zh-CN.json
- Modify: apps/web/src/i18n/locales/en.json
- Modify: apps/web/src/i18n/locales/zh-TW.json
- Modify: apps/web/src/i18n/locales/ru.json


- [ ] **Step 1: 写菜单和结果行为失败测试。**

First add `exportSelectedAuthFiles: vi.fn()` to the existing `mocks` object and expose it in the existing `usageServiceApi` mock. Mock `downloadBlob` with the same existing-module pattern used by `UsageMaintenanceTransferView` tests. The mocked selection state must remain a `Set<string>`, matching `useAuthFilesData`.

~~~tsx
it('adds copy auth JSON to More and copies only the records array', async () => {
  const first = makeCodexFile('a.json', '1', 'a@example.com');
  const second = makeCodexFile('b.json', '2', 'b@example.com');
  mocks.files = [first, second];
  mocks.selectedFiles = new Set([
    getAuthFileSelectionKey(first),
    getAuthFileSelectionKey(second),
  ]);
  mocks.selectionCount = 2;
  mocks.exportSelectedAuthFiles.mockResolvedValue({
    requested: 2,
    exported: 2,
    records: [
      { type: 'codex', email: 'a@example.com' },
      { type: 'codex', email: 'b@example.com' },
    ],
    failed: [],
  });
  const renderer = await renderAccountsPage();
  mocks.loadFiles.mockClear();
  mocks.resetQuota.mockClear();
  mocks.getAccountHistory.mockClear();
  mocks.getAccountWindowUsage.mockClear();
  await act(async () => {
    await findBatchMoreItem(renderer, 'copy-auth-json').onClick();
  });
  expect(mocks.exportSelectedAuthFiles).toHaveBeenCalledTimes(1);
  expect(copyToClipboard).toHaveBeenCalledWith(
    '[{"type":"codex","email":"a@example.com"},{"type":"codex","email":"b@example.com"}]'
  );
  expect(mocks.resetQuota).not.toHaveBeenCalled();
  expect(mocks.getAccountHistory).not.toHaveBeenCalled();
  expect(mocks.getAccountWindowUsage).not.toHaveBeenCalled();
  expect(mocks.loadFiles).not.toHaveBeenCalled();
});

it('opens JSON fallback only when clipboard write fails', async () => {
  const file = makeCodexFile('a.json', '1', 'a@example.com');
  mocks.files = [file];
  mocks.selectedFiles = new Set([getAuthFileSelectionKey(file)]);
  mocks.selectionCount = 1;
  vi.mocked(copyToClipboard).mockResolvedValue(false);
  mocks.exportSelectedAuthFiles.mockResolvedValue({
    requested: 1,
    exported: 1,
    records: [{ type: 'codex', email: 'a@example.com' }],
    failed: [],
  });
  const renderer = await renderAccountsPage();
  await act(async () => {
    await findBatchMoreItem(renderer, 'copy-auth-json').onClick();
  });
  expect(treeText(renderer)).toContain(
    '[{"type":"codex","email":"a@example.com"}]'
  );
});
~~~

- [ ] **Step 2: 运行 UI 测试，确认菜单和组件不存在。**

Run: npm --workspace apps/web run test -- src/features/accounts/components/AuthJsonExportResultModal.test.tsx src/features/accounts/AccountsPage.test.tsx

Expected: FAIL，copy-auth-json 菜单项、导出 API mock 或结果弹窗不存在。

- [ ] **Step 3: 创建只在内存保存 JSON 的结果组件。**

~~~tsx
export type AuthJsonExportDialogState = {
  requested: number;
  exported: number;
  failed: AuthJsonExportFailureItem[];
  jsonText: string | null;
};

type Props = {
  state: AuthJsonExportDialogState | null;
  onClose: () => void;
  onRetryCopy: () => void;
  onDownload: () => void;
  copyRetrying: boolean;
};

export function AuthJsonExportResultModal({
  state,
  onClose,
  onRetryCopy,
  onDownload,
  copyRetrying,
}: Props) {
  const { t } = useTranslation();
  const hasJson = Boolean(state?.jsonText);
  return (
    <Modal
      open={state !== null}
      onClose={onClose}
      title={t(
        hasJson
          ? 'auth_files.copy_export_clipboard_title'
          : 'auth_files.copy_export_failure_title'
      )}
      width={680}
    >
      <div className={styles.root}>
        <p>
          {t('auth_files.copy_export_summary', {
            exported: state?.exported ?? 0,
            requested: state?.requested ?? 0,
          })}
        </p>
        {hasJson ? (
          <textarea
            className={styles.json}
            value={state?.jsonText ?? ''}
            readOnly
            spellCheck={false}
            aria-label={t('auth_files.copy_export_json_label')}
          />
        ) : null}
        {state?.failed.length ? (
          <ul className={styles.failures}>
            {state.failed.map((item) => (
              <li key={item.clientKey}>
                {item.fileName}：{
                  t('auth_files.copy_export_code_' + item.code, {
                    defaultValue: item.code,
                  })
                }
              </li>
            ))}
          </ul>
        ) : null}
      </div>
      {hasJson ? (
        <div className={styles.footer}>
          <Button variant="secondary" onClick={onDownload}>
            {t('auth_files.copy_export_download')}
          </Button>
          <Button onClick={onRetryCopy} loading={copyRetrying}>
            {t('auth_files.copy_export_retry')}
          </Button>
        </div>
      ) : null}
    </Modal>
  );
}
~~~

The parent clears the entire dialog state on close. A partial-success dialog has jsonText: null and exposes only safe filename/code data. The component must not use localStorage, URL state, analytics, an error store, or diagnostics.

- [ ] **Step 4: 接入 AccountsPage，并保持它是一条独立可防重的批量读操作。**

Add imports and state:

~~~tsx
import {
  AuthJsonExportResultModal,
  type AuthJsonExportDialogState,
} from '@/features/accounts/components/AuthJsonExportResultModal';
import { buildAuthJsonExportTargets } from '@/features/accounts/model/authJsonExportTargets';
import { downloadBlob } from '@/utils/download';

const [authJsonExportSubmitting, setAuthJsonExportSubmitting] = useState(false);
const [authJsonExportRetrying, setAuthJsonExportRetrying] = useState(false);
const [authJsonExportDialog, setAuthJsonExportDialog] =
  useState<AuthJsonExportDialogState | null>(null);

const selectedAuthJsonExportTargets = useMemo(
  () => buildAuthJsonExportTargets(selectedTargetFiles),
  [selectedTargetFiles]
);
~~~

Add this handler:

~~~tsx
const handleBatchCopyAuthJson = useCallback(async () => {
  if (
    authJsonExportSubmitting ||
    !managerRequestScope ||
    selectedAuthJsonExportTargets.length === 0
  ) {
    return;
  }
  setAuthJsonExportSubmitting(true);
  try {
    const result = await usageServiceApi.exportSelectedAuthFiles(
      managerRequestScope.apiBase,
      managerRequestScope.managementKey,
      selectedAuthJsonExportTargets
    );
    if (result.records.length === 0) {
      setAuthJsonExportDialog({
        requested: result.requested,
        exported: 0,
        failed: result.failed,
        jsonText: null,
      });
      showNotification(t('auth_files.copy_export_none'), 'error');
      return;
    }
    const jsonText = JSON.stringify(result.records);
    const copied = await copyToClipboard(jsonText);
    if (!copied) {
      setAuthJsonExportDialog({
        requested: result.requested,
        exported: result.exported,
        failed: result.failed,
        jsonText,
      });
      showNotification(t('auth_files.copy_export_clipboard_failed'), 'error');
      return;
    }
    if (result.failed.length) {
      setAuthJsonExportDialog({
        requested: result.requested,
        exported: result.exported,
        failed: result.failed,
        jsonText: null,
      });
      showNotification(
        t('auth_files.copy_export_partial', {
          exported: result.exported,
          requested: result.requested,
          failed: result.failed.length,
        }),
        'success'
      );
      return;
    }
    showNotification(
      t('auth_files.copy_export_success', { count: result.exported }),
      'success'
    );
  } catch (error) {
    const reason = error instanceof Error ? error.message.trim() : '';
    showNotification(reason || t('auth_files.copy_export_failed'), 'error');
  } finally {
    setAuthJsonExportSubmitting(false);
  }
}, [
  authJsonExportSubmitting,
  managerRequestScope,
  selectedAuthJsonExportTargets,
  showNotification,
  t,
]);
~~~

Place this item immediately before existing download in moreItems:

~~~tsx
{
  key: 'copy-auth-json',
  label: authJsonExportSubmitting
    ? t('auth_files.copy_export_preparing', {
        count: selectedAuthJsonExportTargets.length,
      })
    : t('auth_files.copy_export'),
  icon: <IconCopy size={15} />,
  onClick: () => void handleBatchCopyAuthJson(),
  disabled:
    disableControls ||
    authJsonExportSubmitting ||
    !managerRequestScope ||
    selectedAuthJsonExportTargets.length === 0,
},
~~~

Pass disabled={authJsonExportSubmitting} to the floating “更多” DropdownMenu. Render AuthJsonExportResultModal beside AuthJsonPasteModal. The retry handler calls copyToClipboard with authJsonExportDialog.jsonText, clears the dialog only if copying succeeds, and keeps it open if retry fails. The download handler uses:

~~~ts
downloadBlob({
  blob: new Blob([authJsonExportDialog.jsonText], {
    type: 'application/json;charset=utf-8',
  }),
  filename:
    'cpa-auth-export-' +
    new Date().toISOString().replace(/[:.]/g, '-') +
    '.json',
});
~~~

Do not call refreshQuotaRows, loadFiles, inspection refreshes, window-usage, history, automatic recovery, or priority actions from any export path. Do not alter selectedFiles or selectionCount.

- [ ] **Step 5: 加入四种语言的固定文案。**

Add this key family to all four locale files:

~~~text
auth_files.copy_export
auth_files.copy_export_preparing
auth_files.copy_export_success
auth_files.copy_export_partial
auth_files.copy_export_none
auth_files.copy_export_failed
auth_files.copy_export_clipboard_failed
auth_files.copy_export_clipboard_title
auth_files.copy_export_failure_title
auth_files.copy_export_summary
auth_files.copy_export_json_label
auth_files.copy_export_retry
auth_files.copy_export_download
auth_files.copy_export_code_credential_not_found
auth_files.copy_export_code_identity_mismatch
auth_files.copy_export_code_invalid_source_json
auth_files.copy_export_code_source_unavailable
auth_files.copy_export_code_runtime_only
auth_files.copy_export_code_invalid_target
auth_files.copy_export_code_demo_mode
~~~

Chinese fixed error-code translations:

~~~text
credential_not_found = 未找到凭证文件
identity_mismatch = 凭证身份已变化
invalid_source_json = 源授权 JSON 无法解析
source_unavailable = 源文件暂时无法读取
runtime_only = 运行时账号没有可导出的物理授权文件
invalid_target = 凭证标识不完整
demo_mode = 演示模式不导出授权数据
~~~

- [ ] **Step 6: 完成 UI 行为测试。**

Add assertions for all cases:

~~~text
全成功：clipboard 恰好收到 JSON.stringify(records)，没有 failed 信封，没有列表刷新或额度刷新。
部分成功：clipboard 仍收到成功 records；结果弹窗只显示失败文件名和固定错误码，textarea 不存在。
零成功：clipboard 不调用；结果弹窗只显示失败详情。
clipboard 失败：textarea 显示精确 JSON；重试成功后弹窗关闭；下载调用 downloadBlob。
导出期间：连续点击菜单项只发起一次 exportSelectedAuthFiles。
导出后：selectedFiles 和 selectionCount 不变；quota、history、window-usage、recent-request API 均未被调用。
~~~

- [ ] **Step 7: 运行专项前端测试、类型检查和构建。**

Run: npm --workspace apps/web run test -- src/services/api/usageService.credentialRuntime.test.ts src/features/accounts/model/authJsonExportTargets.test.ts src/features/accounts/components/AuthJsonExportResultModal.test.tsx src/features/accounts/AccountsPage.test.tsx

Expected: PASS。

Run: npm --workspace apps/web run type-check

Expected: PASS。

Run: npm --workspace apps/web run build

Expected: PASS。

- [ ] **Step 8: 提交前端功能。**

~~~bash
git add apps/web/src/features/accounts/AccountsPage.tsx apps/web/src/features/accounts/AccountsPage.test.tsx apps/web/src/features/accounts/components/AuthJsonExportResultModal.tsx apps/web/src/features/accounts/components/AuthJsonExportResultModal.module.scss apps/web/src/features/accounts/components/AuthJsonExportResultModal.test.tsx apps/web/src/i18n/locales/zh-CN.json apps/web/src/i18n/locales/en.json apps/web/src/i18n/locales/zh-TW.json apps/web/src/i18n/locales/ru.json
git commit -m "feat: copy selected authorization JSON"
~~~


### Task 6: 全量验证与敏感数据边界审计

**Files:**
- Modify only files from Tasks 1–5 if a named test exposes a concrete defect.
- Test: all files added or modified above.

- [ ] **Step 1: 运行完整 Manager Server 测试。**

Run: cd apps/manager-server; go test ./...

Expected: PASS。

- [ ] **Step 2: 运行完整前端测试和发布边界检查。**

Run: npm run test:web

Expected: PASS。

Run: npm run check:demo-isolation; npm run check:web-icons

Expected: 两条命令均 PASS。

- [ ] **Step 3: 在测试用 Manager Server/CPA stub 中做 HTTP 冒烟验证。**

~~~bash
curl -sS -D - -X POST "$MANAGER_TEST_BASE/v0/management/auth-files/export-selected"   -H "Authorization: Bearer $MANAGER_TEST_PANEL_TOKEN"   -H "Content-Type: application/json"   --data '{"targets":[{"clientKey":"smoke-a","fileName":"a.json","provider":"codex","runtimeId":"runtime-a"}]}'
~~~

Expected: 200 OK、Cache-Control: no-store，并且顶层仅有 requested、exported、records、failed；clipboard 输入只会是 records 数组，不会是 response 信封。

- [ ] **Step 4: 审计敏感数据边界与格式。**

Run: rg -n "export-selected|copy-auth-json|AuthJsonExport" apps/manager-server apps/web

Expected: 浏览器请求只引用 clientKey、fileName、authIndex、accountEmail、provider、runtimeId、accountId、accountSnapshot；不出现 access_token、refresh_token、id_token、proxy_url、原始 body 日志或 SQLite 写入。

Run: git diff --check 08d322ca..HEAD

Expected: 无输出，退出码 0。

- [ ] **Step 5: 核对变更范围。**

Run: git status --short; git diff --stat 08d322ca..HEAD; git log --oneline 08d322ca..HEAD

Expected: 仅出现本计划列出的 CPAM Manager Server、Web、i18n、测试和文档文件；不出现 CPA Core、cli-proxy-api、计费存储结构、巡检或部署文件。

If Task 6 changed code only to repair a proven failing test:

~~~bash
git add apps/manager-server apps/web
git commit -m "fix: harden selected auth JSON export"
~~~

## 覆盖审计（计划自检）

| 已确认需求 | 对应任务 |
| --- | --- |
| “更多”菜单新增复制入口，原批量下载保留 | Task 5 |
| 共享物理文件只导出被选成员 | Tasks 1–2 |
| 局部失败仍复制成功记录且可查看失败文件 | Tasks 2、5 |
| 正常直接复制，剪贴板失败才显示 JSON 兜底 | Task 5 |
| 300 条、48 MiB、8 路读取 | Task 2 |
| 管理员鉴权、身份白名单、no-store、无持久化/日志泄漏 | Tasks 2–3、6 |
| 不触发额度、历史、巡检、调度、计费或列表刷新 | Task 5 |
| 不改 CPA Core，保留全部既有二改 | 全部任务的范围约束与 Task 6 |

实施基线说明：本隔离 worktree 的依赖已安装。此前一次宽泛 npm run test:web 因工作站上已有 Vitest 进程竞争而被停止，不能作为通过证据。实施时必须按每项专项命令先建绿，再执行 Task 6 全量验证；不得把此前被停止的命令视为通过。
