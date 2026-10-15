# 已启用筛选下的完整指标计数 Implementation Plan

> **For agentic workers:** Execute this plan task-by-task in the isolated `lo/custom` worktree. Follow TDD: write the failing test, observe RED, implement the smallest change, then verify GREEN.

**Goal:** When the credential list is filtered to `已启用`, make the `正常可用` summary card display the complete ready server-side enabled count for the current query instead of only the visible page count.

**Architecture:** Keep the existing 50-row server pagination and all health classification unchanged. Add a pure presentation helper in the account-row model that applies the server query `total` only when the enabled snapshot is ready; the page component calls it after building the existing page-local metrics. During warming or legacy fallback, preserve current local metrics.

**Tech Stack:** React + TypeScript, Vitest, Vite, existing CPAM Manager Server credential-list projection.

---

### Task 1: Add the failing model tests

**Files:**
- Modify: `apps/web/src/features/accounts/model/accountRows.test.ts`

- [ ] **Step 1: Import the new helper**

Add `applyEnabledCredentialListMetric` to the existing import from `./accountRows`.

- [ ] **Step 2: Add the RED tests**

Append tests near the existing `buildAccountMetrics` tests:

```ts
it('uses the ready enabled query total for the normal-available card', () => {
  const metrics = applyEnabledCredentialListMetric(
    { total: 50, available: 45, needsAttention: 0, quotaRisk: 5, disabled: 0, unconfirmed: 0, needsInspectionAction: 0 },
    { status: 'enabled', serverPaged: true, syncStatus: 'ready', total: 292 }
  );

  expect(metrics.available).toBe(292);
  expect(metrics.total).toBe(50);
});

it('keeps page-local metrics while the enabled snapshot is warming', () => {
  const metrics = applyEnabledCredentialListMetric(
    { total: 50, available: 45, needsAttention: 0, quotaRisk: 5, disabled: 0, unconfirmed: 0, needsInspectionAction: 0 },
    { status: 'enabled', serverPaged: true, syncStatus: 'warming', total: 35 }
  );

  expect(metrics.available).toBe(45);
});

it('does not replace health metrics for other status filters', () => {
  const metrics = applyEnabledCredentialListMetric(
    { total: 50, available: 45, needsAttention: 0, quotaRisk: 5, disabled: 0, unconfirmed: 0, needsInspectionAction: 0 },
    { status: 'available', serverPaged: true, syncStatus: 'ready', total: 202 }
  );

  expect(metrics.available).toBe(45);
});
```

- [ ] **Step 3: Run the focused test and verify RED**

Run from `apps/web`:

```powershell
npx --no-install vitest run src/features/accounts/model/accountRows.test.ts --pool=threads --maxWorkers=1
```

Expected: TypeScript/test failure because `applyEnabledCredentialListMetric` does not exist yet.

### Task 2: Implement the pure metric override

**Files:**
- Modify: `apps/web/src/features/accounts/model/accountRows.ts`

- [ ] **Step 1: Add the typed options and helper after `buildAccountMetrics`**

```ts
export interface EnabledCredentialListMetricOptions {
  status: AccountStatusFilter;
  serverPaged: boolean;
  syncStatus?: string;
  total?: number;
}

export const applyEnabledCredentialListMetric = (
  metrics: AccountMetrics,
  options: EnabledCredentialListMetricOptions
): AccountMetrics => {
  if (
    options.status !== 'enabled' ||
    !options.serverPaged ||
    options.syncStatus !== 'ready' ||
    !Number.isInteger(options.total) ||
    options.total < 0
  ) {
    return metrics;
  }
  return { ...metrics, available: options.total };
};
```

- [ ] **Step 2: Run the focused tests and verify GREEN**

```powershell
npx --no-install vitest run src/features/accounts/model/accountRows.test.ts --pool=threads --maxWorkers=1
```

Expected: all tests in the file pass.

### Task 3: Wire the helper into the account page

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`

- [ ] **Step 1: Import `applyEnabledCredentialListMetric`**

Add it to the existing account-row model import.

- [ ] **Step 2: Apply the ready server total after local metrics are built**

Change the existing `metrics` `useMemo` so it first constructs the current `localMetrics`, preserves the existing `inventory_total` override for `total`, and then returns:

```ts
return applyEnabledCredentialListMetric(metricsWithInventoryTotal, {
  status: statusFilter,
  serverPaged: serverCredentialListEnabled,
  syncStatus: serverCredentialListPage.data?.sync_status,
  total: serverCredentialListPage.data?.total,
});
```

Add `statusFilter` to the dependency array. Do not change `buildAccountMetrics`, filtering, pagination, quota loading, or usage paths.

- [ ] **Step 3: Run focused web tests**

```powershell
npx --no-install vitest run \
  src/features/accounts/model/accountRows.test.ts \
  src/features/accounts/model/credentialListQuery.test.ts \
  src/features/accounts/hooks/useCredentialListPage.test.tsx \
  src/features/accounts/components/AccountProviderTabs.test.tsx \
  --pool=threads --maxWorkers=1
```

Expected: all selected files pass.

### Task 4: Type-check and build

**Files:** none additional.

- [ ] **Step 1: Type-check**

```powershell
npx --no-install tsc --noEmit --pretty false
```

Expected: exit code 0.

- [ ] **Step 2: Build the web application**

```powershell
npm run build
```

Expected: exit code 0 and a completed production bundle.

- [ ] **Step 3: Review the diff**

```powershell
git diff --check
git status --short
```

Expected: only the model test, model implementation, and `AccountsPage.tsx` are modified before commit.

### Task 5: Commit, push, and CPAM-only deployment

- [ ] **Step 1: Commit the implementation**

```powershell
git add apps/web/src/features/accounts/model/accountRows.ts apps/web/src/features/accounts/model/accountRows.test.ts apps/web/src/features/accounts/AccountsPage.tsx
git commit -m "fix: show full enabled count in account metrics"
```

- [ ] **Step 2: Push only the custom branch**

```powershell
git push origin lo/custom
```

Do not push to `upstream` and do not touch CPA Core.

- [ ] **Step 3: Build/deploy only `cpa-manager-plus`**

Use the existing CPAM deployment runbook and protected promotion script. Before and after promotion, verify the CPA Core container identity is unchanged. On the VPS restart only `cpam.service`; preserve `/home/cpam/data`, SQLite, secrets, and OpenResty configuration.

- [ ] **Step 4: Verify behavior**

Select `已启用`, wait for `sync_status=ready`, and verify:

- pagination still shows 50 rows per page;
- the page total is the full enabled query total (for example 292);
- `正常可用` shows the same enabled total (292), not the current page’s 45;
- selecting `可用` still uses the original health filter;
- `/health` remains HTTP 200;
- CPA Core container ID/image are unchanged.
