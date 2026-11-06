# Proxy Current-Enabled Account Count Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the proxy-overload modal display, for each existing 72-hour observed proxy, the number of credentials currently enabled and bound to that proxy, including no-request and pending/unknown credentials, without changing any 72-hour activity, overload, success-rate, sort, copy, or saved-proxy behavior.

**Architecture:** The Manager already fetches the complete CPA inventory once. A new `current_enabled_account_count` is calculated directly from that full inventory before strict history matching; the existing history aggregate stays untouched and merely reads the precomputed count when serializing each observed row. The web client uses the new additive field for its displayed “当前已启用” label, with a temporary fallback to the existing active-only field for mixed-version deployments.

**Tech Stack:** Go Manager Server, current CPA auth-file inventory client, React/TypeScript, Axios response types, i18next locale JSON, Go tests, Vitest, TypeScript type-check, Vite production build.

---

## Files and Responsibilities

| File | Responsibility |
| --- | --- |
| `apps/manager-server/internal/service/proxyoverload/service.go` | Add the additive current-inventory summary/row counters while preserving every historical metric. |
| `apps/manager-server/internal/service/proxyoverload/service_test.go` | Prove pending/no-evidence and strict-history-ineligible enabled credentials affect only the new binding count. |
| `apps/web/src/services/api/usageService.ts` | Add optional response fields and realistic demo data. |
| `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx` | Prefer current-inventory counters in the existing summary and active-account cell. |
| `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx` | Prove display preference and old-Manager fallback without changing table behavior. |
| `apps/web/src/i18n/locales/{zh-CN,zh-TW,en,ru}.json` | Rename the displayed metric to “当前已启用” and clarify the differing scopes. |
| `docs/LO-CPAM-CUSTOM-MEMORY.md` | Persist the invariant that current enabled binding is independent of 72-hour history evidence. |

## Fixed API Contract

```go
type Summary struct {
    ActiveAccountCount         int `json:"active_account_count"`
    EnabledActiveAccountCount  int `json:"enabled_active_account_count"` // existing: active-only
    CurrentEnabledAccountCount int `json:"current_enabled_account_count"` // new: all enabled + bound
}

type ProxyRow struct {
    ActiveAccountCount         int `json:"active_account_count"`
    EnabledActiveAccountCount  int `json:"enabled_active_account_count"` // existing: active-only
    CurrentEnabledAccountCount int `json:"current_enabled_account_count"` // new: all enabled + same proxy
}
```

```ts
export interface ProxyOverloadSummaryRow {
  proxy_url: string;
  active_account_count: number;
  enabled_active_account_count?: number;
  current_enabled_account_count?: number;
  overloaded_account_count: number;
  non_overloaded_account_count: number;
  overloaded_rate: number;
}

export interface ProxyOverloadSummaryResponse {
  active_account_count: number;
  enabled_active_account_count?: number;
  current_enabled_account_count?: number;
  // existing fields unchanged
}
```

The old field is not deleted or redefined. The new field counts only inventory items that are `Disabled == false` and have a normalized, non-empty current `proxy_url`. It includes current enabled credentials even when no request event exists, `latestRequestTarget` rejects their historical identity, or their credential-list UI label is pending/unknown.

### Task 1: Add the backend regression and current-inventory aggregation

**Files:**
- Modify: `apps/manager-server/internal/service/proxyoverload/service.go`
- Modify: `apps/manager-server/internal/service/proxyoverload/service_test.go`

- [x] **Step 1: Establish the untouched focused-test baseline**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/service/proxyoverload -count=1
```

Expected: `PASS` before adding the regression.

- [x] **Step 2: Write the failing current-binding test**

Add `TestSummaryCountsCurrentEnabledBindingsOutsideActiveEvidence` beside `TestSummaryCountsEnabledSubsetWithoutChangingExistingTotals`. Use one current proxy with three enabled files and one disabled file, but supply active evidence for only one enabled file. Mark another enabled file `AccountIDInvalid = true` so it is deliberately excluded from strict history targets but must still count as a current binding.

```go
func TestSummaryCountsCurrentEnabledBindingsOutsideActiveEvidence(t *testing.T) {
    now := time.Date(2026, time.November, 6, 12, 0, 0, 0, time.UTC)
    historyIneligible := testCodexFile(
        "history-ineligible.json", "3", "ineligible@example.test", "workspace-c", "http://proxy-a.example:8080",
    )
    historyIneligible.AccountIDInvalid = true
    disabled := testCodexFile(
        "disabled.json", "4", "disabled@example.test", "workspace-d", "http://proxy-a.example:8080",
    )
    disabled.Disabled = true
    inventory := &fakeInventoryReader{files: []cpaauthfiles.File{
        testCodexFile("active.json", "1", "active@example.test", "workspace-a", "http://proxy-a.example:8080/"),
        testCodexFile("no-event.json", "2", "no-event@example.test", "workspace-b", "http://proxy-a.example:8080////"),
        historyIneligible,
        disabled,
        testCodexFile("other-active.json", "5", "other@example.test", "workspace-e", "http://proxy-b.example:8080"),
    }}
    evidence := &fakeEvidenceReader{items: []store.ProxyOverloadEvidence{
        {RequestIndex: 0, Active: true},
        {RequestIndex: 3, Active: true, ServerOverloaded: true},
    }}
    service := New(Options{
        SetupResolver: fakeSetupResolver{setup: configuredSetup(), ok: true},
        AuthFiles: inventory,
        Evidence: evidence,
        Now: func() time.Time { return now },
    })

    result, err := service.Summary(context.Background(), true)
    if err != nil { t.Fatalf("summary: %v", err) }
    if result.ActiveAccountCount != 2 || result.EnabledActiveAccountCount != 2 {
        t.Fatalf("active current subset = %#v", result)
    }
    if result.CurrentEnabledAccountCount != 4 {
        t.Fatalf("current enabled bindings = %d, want 4", result.CurrentEnabledAccountCount)
    }
    rows := make(map[string]ProxyRow, len(result.Proxies))
    for _, row := range result.Proxies { rows[row.ProxyURL] = row }
    if got := rows["http://proxy-a.example:8080/"]; got.CurrentEnabledAccountCount != 3 || got.ActiveAccountCount != 1 {
        t.Fatalf("proxy a = %#v", got)
    }
    if got := rows["http://proxy-b.example:8080"]; got.CurrentEnabledAccountCount != 1 || got.OverloadedAccountCount != 1 {
        t.Fatalf("proxy b = %#v", got)
    }
    if inventory.callCount() != 1 || evidence.callCount() != 1 {
        t.Fatalf("inventory/evidence calls = %d/%d, want 1/1", inventory.callCount(), evidence.callCount())
    }
}
```

- [x] **Step 3: Run only the new regression and confirm it fails**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/service/proxyoverload -run TestSummaryCountsCurrentEnabledBindingsOutsideActiveEvidence -count=1
```

Expected: compile failure because `CurrentEnabledAccountCount` does not yet exist.

- [x] **Step 4: Add the minimal additive backend contract**

In `Summary`, `ProxyRow`, and `proxyAggregate`, add `CurrentEnabledAccountCount` / `currentEnabledCount` with the exact JSON key `current_enabled_account_count`.

Move result initialization to immediately after the one inventory fetch, then derive the direct current inventory map before building `targets`:

```go
now := s.now()
result := Summary{
    GeneratedAtMS:           now.UnixMilli(),
    WindowStartMS:           now.Add(-summaryWindow).UnixMilli(),
    Proxies:                 []ProxyRow{},
    SavedProxyListAvailable: savedProxyListAvailable,
    UnobservedProxies:       []string{},
}
currentEnabledByProxy := make(map[string]int, len(files))
for _, file := range files {
    if file.Disabled {
        continue
    }
    key, _ := normalizeProxyURL(file.ProxyURL)
    if key == "" {
        continue
    }
    result.CurrentEnabledAccountCount++
    currentEnabledByProxy[key]++
}
```

Keep `targets` / `filesByTarget` construction exactly as it is. In the existing active-evidence loop, retain the old `EnabledActiveAccountCount` increments unchanged. When an active group is created, attach the precomputed current binding count:

```go
group := groups[key]
if group == nil {
    group = &proxyAggregate{
        key:                 key,
        proxyURL:            displayURL,
        currentEnabledCount: currentEnabledByProxy[key],
    }
    groups[key] = group
}
```

Serialize it without changing rates or sorting:

```go
CurrentEnabledAccountCount: group.currentEnabledCount,
```

Do not prepopulate `groups` from `currentEnabledByProxy`; only active evidence creates observable proxy rows. Do not alter `decorateSavedProxyDifference`, `latestRequestTarget`, the 72-hour limit, nine-event cap, or any use of `EnabledActiveAccountCount`.

- [x] **Step 5: Verify backend behavior and commit it**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/service/proxyoverload -count=1
Set-Location ../..
git add apps/manager-server/internal/service/proxyoverload/service.go apps/manager-server/internal/service/proxyoverload/service_test.go
git commit -m "feat: expose current proxy enabled bindings"
```

Expected: focused package passes; one commit contains only backend implementation and regression.

### Task 2: Prefer current bindings in the web API and modal

**Files:**
- Modify: `apps/web/src/services/api/usageService.ts`
- Modify: `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx`
- Modify: `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/ru.json`

- [x] **Step 1: Write failing modal display and fallback tests**

Update the `result` fixture so active-only counts differ from current binding counts:

```ts
active_account_count: 4,
enabled_active_account_count: 2,
current_enabled_account_count: 6,
// healthy row
active_account_count: 2,
enabled_active_account_count: 2,
current_enabled_account_count: 4,
```

Change the first renderer assertions to require the new values:

```ts
expect(
  renderer!.root.findAll((node) =>
    hasText(node.props.children, 'accounts.proxy_overload_active:4:6')
  ).length
).toBeGreaterThan(0);
expect(
  renderer!.root.findAll((node) =>
    hasText(node.props.children, 'accounts.proxy_overload_active_cell:2:4')
  ).length
).toBeGreaterThan(0);
```

Add an old-Manager fallback test that removes only `current_enabled_account_count`, preserves `enabled_active_account_count`, and expects `:4:2` and `:2:2`. Keep the existing all-omitted test and make it expect zero.

- [x] **Step 2: Run the focused modal test and confirm it fails**

Run:

```powershell
npm --prefix apps/web test -- --run src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx
```

Expected: assertions fail because the current UI still renders `enabled_active_account_count`.

- [x] **Step 3: Extend client response types and demo data**

In `usageService.ts`, add optional fields to both response interfaces:

```ts
current_enabled_account_count?: number;
```

In the demo response, provide the new summary and row values explicitly. Do not change endpoint URLs, query parameters, timeouts, fetch cadence, or the proxy sorting model.

- [x] **Step 4: Make only the displayed enabled value prefer the new field**

In `ProxyOverloadSummaryModal.tsx`, replace the old summary variable with:

```ts
const currentEnabledCount =
  result?.current_enabled_account_count ?? result?.enabled_active_account_count ?? 0;
```

Use it in `proxy_overload_active`. For each observed row, compute the local value without changing the row’s active count or its rate:

```tsx
const currentEnabledCount =
  row.current_enabled_account_count ?? row.enabled_active_account_count ?? 0;

{t('accounts.proxy_overload_active_cell', {
  count: row.active_account_count,
  enabled: currentEnabledCount,
})}
```

Use a block callback for `observedRows.map` so the per-row local variable is scoped safely. Keep filters, selection keys, sorting, copy text, unobserved rows, and success-rate calculation byte-for-byte semantically unchanged.

- [x] **Step 5: Update all existing locale keys, not the table shape**

Replace the existing values for `proxy_overload_active`, `proxy_overload_active_cell`, and `proxy_overload_hint` in all four locale JSON files. Use these Chinese values:

```json
"proxy_overload_hint": "统计最近 3 天内有请求的账号；每个账号最多检查最近 9 条请求，并按当前代理 URL 归属。仅 HTTP 502 且错误码为 server_is_overloaded 计为服务过载。当前已启用按当前绑定该代理且未禁用的账号统计，包含待判定和无最近请求账号；活跃、过载与成功率仍只按最近 3 天请求统计。",
"proxy_overload_active": "活跃账号 {{count}} 个（当前已启用 {{enabled}} 个）",
"proxy_overload_active_cell": "{{count}}（当前已启用 {{enabled}}）"
```

Use equivalent localized wording in `zh-TW`, `en`, and `ru`. Do not add a table column or change its width.

- [x] **Step 6: Verify web behavior and commit it**

Run:

```powershell
npm --prefix apps/web test -- --run src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx
npm --prefix apps/web run type-check
npm --prefix apps/web run build
git add apps/web/src/services/api/usageService.ts apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx apps/web/src/i18n/locales/zh-CN.json apps/web/src/i18n/locales/zh-TW.json apps/web/src/i18n/locales/en.json apps/web/src/i18n/locales/ru.json
git commit -m "feat: show current enabled proxy bindings"
```

Expected: the focused modal suite, type check, and production bundle all pass; one commit contains only web contract/rendering/localization changes.

### Task 3: Record the invariant and run cross-layer verification

**Files:**
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`

- [x] **Step 1: Add the retained invariant to custom memory**

Append a dated subsection stating all of the following explicitly:

```markdown
## 2026-11-06: Proxy current-enabled binding count (must retain)

- `current_enabled_account_count` is an additive, current-CPA-inventory metric. It counts every non-disabled credential with a non-empty normalized current proxy URL, including no-request, pending/unknown, and history-identity-ineligible credentials.
- It must never redefine `enabled_active_account_count`; the latter remains the enabled subset of strictly matched 72-hour active evidence for compatibility.
- The proxy-overload UI displays the new current count as `当前已启用`, but overload, non-overload, rate, filter, sort, copy, saved/unobserved proxies, and observed row existence remain based on the prior 72-hour logic.
- Derive both counts from the single existing inventory fetch. Do not add a per-account CPA call or SQLite query, and do not create a row for a proxy with current bindings only and no 72-hour active evidence.
```

- [x] **Step 2: Run the combined targeted suite**

Run:

```powershell
podman run --rm -v "${PWD}:/workspace" -w /workspace/apps/manager-server golang:1.26.10-bookworm go test ./internal/service/proxyoverload -count=1
npm --prefix apps/web test -- --run src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx
npm --prefix apps/web run type-check
npm --prefix apps/web run build
git diff --check
git status --short
```

Expected: all four validations pass, `git diff --check` has no output, and the only uncommitted file is the custom-memory update.

- [x] **Step 3: Commit the invariant record**

Run:

```powershell
git add docs/LO-CPAM-CUSTOM-MEMORY.md
git commit -m "docs: retain current proxy enabled binding invariant"
git status --short --branch
```

Expected: clean worktree; no change outside CPAM.

### Task 4: Release only after implementation verification is green

**Files:**
- No source changes.
- Reference: `CPAM_UPDATE_RUNBOOK.md` only if the user explicitly requests “更新 CPAM” / “更新cpam”.

- [ ] **Step 1: Capture candidate identity before deployment**

Run:

```powershell
git log -1 --format='%H%n%s'
git status --short --branch
```

Expected: a clean `lo/custom` candidate with all three implementation/memory commits present.

- [ ] **Step 2: Follow CPAM-only deployment boundaries**

When deployment is requested, use the established CPAM deployment flow:

```text
1. Record local CPA Core container identity and remote cpa.service PID/state.
2. Rebuild/recreate only local cpa-manager-plus from the verified candidate.
3. Atomically deploy only /home/cpam/app/cpa-manager-plus remotely.
4. Restart only cpam.service.
5. Recheck that local Core and remote cpa.service identities did not change.
```

Never restart, build, modify, or deploy CPA Core / `cli-proxy-api`; never touch `/home/cpa`, `cpa.service`, SQLite usage history, `data.key`, OpenResty, certificates, or unrelated services.

- [ ] **Step 3: Perform post-deployment behavior verification**

Use a panel-authenticated request to the existing endpoint:

```text
GET /v0/management/monitoring/proxy-overload-summary?refresh=1
```

Verify each response contains both `enabled_active_account_count` and `current_enabled_account_count`. Identify a proxy whose current-enabled count exceeds its active-enabled count and confirm the modal shows the former under `当前已启用` while success rate remains calculated from `active_account_count` and `non_overloaded_account_count`.

Confirm `/health` succeeds, `cpam.service` is active, and CPA Core remains unchanged.