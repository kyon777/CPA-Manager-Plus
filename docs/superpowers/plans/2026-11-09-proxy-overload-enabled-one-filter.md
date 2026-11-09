# Proxy Overload Exactly-One Enabled Filter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Add a front-end-only `已启用(1)` proxy-overload filter that shows observed rows with exactly one current enabled account.

**Architecture:** Extend the existing `ProxyOverloadFilter` union with `enabled_one`, then apply the same existing enabled-count compatibility resolution (`current_enabled_account_count ?? enabled_active_account_count ?? 0`) in the model predicate. The modal will include this observed-row filter after `unenabled`; it reuses the existing table, copy, sorting, and local state paths without backend changes.

**Tech Stack:** React 19, TypeScript, Vitest, react-test-renderer, i18next JSON resources.

---

## File Structure

- Modify `apps/web/src/features/accounts/model/proxyOverloadSummary.ts`: define the `enabled_one` filter and exact-one predicate.
- Modify `apps/web/src/features/accounts/model/proxyOverloadSummary.test.ts`: prove exact-one filtering and compatibility fallback.
- Modify `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx`: render the tab after `unenabled`.
- Modify `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx`: prove tab activation filters locally without refresh.
- Modify `apps/web/src/i18n/locales/{en,ru,zh-CN,zh-TW}.json`: add the new visible label.

### Task 1: Model exact-one filter

**Files:**
- Modify: `apps/web/src/features/accounts/model/proxyOverloadSummary.test.ts`
- Modify: `apps/web/src/features/accounts/model/proxyOverloadSummary.ts`

- [x] **Step 1: Write the failing model test**

Add fixtures covering current count `0`, current count `1`, current count `2`, and legacy `enabled_active_account_count: 1`; assert:

```ts
expect(filterProxyOverloadRows(rows, 'enabled_one').map((row) => row.proxy_url)).toEqual([
  'http://enabled_one-current:8080',
  'http://enabled_one-legacy:8080',
]);
```

- [x] **Step 2: Verify RED**

Run:

```powershell
npm --workspace apps/web run test -- src/features/accounts/model/proxyOverloadSummary.test.ts
```

Expected: TypeScript/Vitest fails because `enabled_one` is not a `ProxyOverloadFilter` value.

- [x] **Step 3: Implement the minimal model predicate**

Add `enabled_one` to the union and switch:

```ts
case 'enabled_one':
  return rows.filter(
    (row) => (row.current_enabled_account_count ?? row.enabled_active_account_count ?? 0) === 1
  );
```

Do not alter `unenabled`, `unobserved`, sorting, or backend behavior.

- [x] **Step 4: Verify GREEN**

Run the Task 1 test command again. Expected: model test passes.

- [x] **Step 5: Commit the model behavior**

```powershell
git add apps/web/src/features/accounts/model/proxyOverloadSummary.ts apps/web/src/features/accounts/model/proxyOverloadSummary.test.ts
git commit -m "feat: filter proxies with one enabled account"
```

### Task 2: Modal tab and translations

**Files:**
- Modify: `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx`
- Modify: `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/ru.json`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`

- [x] **Step 1: Write the failing modal test**

Click the button keyed by `accounts.proxy_overload_filter_enabled_one`, then assert that the rendered proxy URL set contains only the exact-one fixture and that `onRefresh` remains uncalled.

- [x] **Step 2: Verify RED**

Run:

```powershell
npm --workspace apps/web run test -- src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx
```

Expected: the new button cannot be found.

- [x] **Step 3: Implement the tab and labels**

Insert `enabled_one` after `unenabled` in both modal filter arrays. Add the translation key `proxy_overload_filter_enabled_one` with values `Enabled (1)`, `Enabled (1)`, `已启用(1)`, and `已啟用(1)`.

- [x] **Step 4: Verify focused tests, type-check, and web build**

Run:

```powershell
npm --workspace apps/web run test -- src/features/accounts/model/proxyOverloadSummary.test.ts src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx
npm --workspace apps/web run type-check
npm --workspace apps/web run build
```

Expected: all commands exit `0`.

- [x] **Step 5: Commit the UI implementation**

```powershell
git add apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx apps/web/src/i18n/locales/en.json apps/web/src/i18n/locales/ru.json apps/web/src/i18n/locales/zh-CN.json apps/web/src/i18n/locales/zh-TW.json
git commit -m "feat: add exact-one enabled proxy filter"
```