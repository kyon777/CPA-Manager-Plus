# Proxy Overload Unenabled Filter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a local-only “Unenabled” proxy-overload filter for observed rows with no current enabled account binding.

**Architecture:** Extend the existing `ProxyOverloadFilter` union and the model’s in-memory switch predicate. The modal will render the new observed-row filter button and reuse its existing filter, table, checkbox, copy, and refresh behavior. No backend/API/database changes are needed.

**Tech Stack:** React 19, TypeScript, Vitest, react-test-renderer, i18next JSON resources.

---

## File Structure

- Modify `apps/web/src/features/accounts/model/proxyOverloadSummary.ts`: define the `unenabled` filter and resolve the existing enabled-count compatibility fallback for predicate use.
- Modify `apps/web/src/features/accounts/model/proxyOverloadSummary.test.ts`: prove observed-only zero-current-enabled filtering and unobserved separation.
- Modify `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx`: include `unenabled` in the observed filter buttons.
- Modify `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx`: prove local modal selection/rendering does not refresh.
- Modify `apps/web/src/i18n/locales/{en,ru,zh-CN,zh-TW}.json`: add the filter label.

### Task 1: Model predicate and regression coverage

**Files:**
- Modify: `apps/web/src/features/accounts/model/proxyOverloadSummary.test.ts`
- Modify: `apps/web/src/features/accounts/model/proxyOverloadSummary.ts`

- [x] **Step 1: Write the failing model test**

Add observed fixtures with `current_enabled_account_count` values `0`, `1`, and absent-with-`enabled_active_account_count: 0`; assert:

```ts
expect(filterProxyOverloadRows(rows, 'unenabled').map((row) => row.proxy_url)).toEqual([
  'http://unenabled-current:8080',
  'http://unenabled-legacy:8080',
]);
expect(visibleProxyOverloadRows(rows, unobservedRows, 'unobserved')).toEqual(unobservedRows);
```

- [x] **Step 2: Run the focused model test to verify RED**

Run:

```powershell
npm --workspace apps/web run test -- src/features/accounts/model/proxyOverloadSummary.test.ts
```

Expected: TypeScript/Vitest fails because `'unenabled'` is not a `ProxyOverloadFilter` value and/or the predicate does not filter it.

- [x] **Step 3: Implement the minimal predicate**

Add `unenabled` to `ProxyOverloadFilter` and a `case 'unenabled'` returning rows where:

```ts
const enabled = row.current_enabled_account_count ?? row.enabled_active_account_count ?? 0;
return enabled === 0;
```

Keep `unobserved` handled only by `visibleProxyOverloadRows()`.

- [x] **Step 4: Run the focused model test to verify GREEN**

Run the Task 1 command again. Expected: all model tests pass.

- [x] **Step 5: Commit the model change**

```powershell
git add apps/web/src/features/accounts/model/proxyOverloadSummary.ts apps/web/src/features/accounts/model/proxyOverloadSummary.test.ts
git commit -m "feat: filter unenabled proxy overload rows"
```

### Task 2: Modal filter button, translations, and interaction coverage

**Files:**
- Modify: `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx`
- Modify: `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/ru.json`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`

- [x] **Step 1: Write the failing modal test**

Set one observed fixture’s `current_enabled_account_count` to `0`, click:

```ts
findButton(renderer!, 'accounts.proxy_overload_filter_unenabled').props.onClick();
```

Assert the visible rows contain only that observed proxy and `onRefresh` was not called.

- [x] **Step 2: Run the focused modal test to verify RED**

Run:

```powershell
npm --workspace apps/web run test -- src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx
```

Expected: the new button cannot be found.

- [x] **Step 3: Implement the UI and localizations**

Include `unenabled` between `overloaded` and optional `unobserved` in both filter arrays. Add `proxy_overload_filter_unenabled` translations:

```json
"proxy_overload_filter_unenabled": "Unenabled"
```

Use corresponding Chinese text in zh-CN/zh-TW and preserve every existing filter entry.

- [x] **Step 4: Run focused UI/model verification**

Run:

```powershell
npm --workspace apps/web run test -- src/features/accounts/model/proxyOverloadSummary.test.ts src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx
```

Expected: all focused tests pass.

- [x] **Step 5: Type-check and production-build**

Run:

```powershell
npm --workspace apps/web run type-check
npm --workspace apps/web run build
```

Expected: both exit `0`.

- [x] **Step 6: Commit implementation and translation changes**

```powershell
git add apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx apps/web/src/i18n/locales/en.json apps/web/src/i18n/locales/ru.json apps/web/src/i18n/locales/zh-CN.json apps/web/src/i18n/locales/zh-TW.json
git commit -m "feat: add unenabled proxy overload filter"
```