# Accounts Live Refresh Toggle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a persisted Accounts-list live-refresh switch that freezes front-end observation without affecting CPAM/CPA background execution, billing, credentials, or scheduler behavior.

**Architecture:** The Accounts page owns a browser-local `liveRefreshEnabled` preference and uses it only to gate passive display polling. Explicit context changes (initial page, pagination, filtering, sorting) and manual refresh remain one-shot reads. The server-error priority stream receives a separate `pollingEnabled` input so its cursor survives a user freeze and catches up from the exact acknowledged tuple when the switch is restored.

**Tech Stack:** React 19, TypeScript, Vitest/react-test-renderer, `useLocalStorage`, existing CPAM Manager read APIs.

---

## File structure and boundaries

| File | Responsibility |
|---|---|
| `apps/web/src/features/accounts/hooks/useServerErrorPriorityUpdates.ts` | Preserve the durable priority-update cursor through a front-end freeze while still aborting in-flight work and resetting on true scope/lifecycle invalidation. |
| `apps/web/src/features/accounts/hooks/useServerErrorPriorityUpdates.test.tsx` | Prove pause/resume cursor continuation and scope reset behavior. |
| `apps/web/src/features/accounts/AccountsPage.tsx` | Persist and render the Accounts switch; gate passive 10/30/60-second streams and trigger one explicit catch-up when re-enabled. |
| `apps/web/src/features/accounts/AccountsPage.test.tsx` | Prove timer suppression, manual one-shot refresh, re-enable catch-up, and browser-local preference/UI state. |
| `apps/web/src/features/accounts/AccountsPage.module.scss` | Keep the switch and frozen snapshot indicator compact, wrapping safely in the existing batch toolbar. |
| `apps/web/src/i18n/locales/{en,zh-CN,zh-TW,ru}.json` | Add all visible Accounts-list switch labels without raw fallback keys. |
| `docs/LO-CPAM-CUSTOM-MEMORY.md` | Preserve the CPAM-only behavior for future upstream merges. |

## Task 1: Preserve priority-update cursor while the list is frozen

**Files:**
- Modify: `apps/web/src/features/accounts/hooks/useServerErrorPriorityUpdates.ts`
- Modify: `apps/web/src/features/accounts/hooks/useServerErrorPriorityUpdates.test.tsx`

- [x] **Step 1: Extend the hook test harness with the new passive-poll gate and write the failing resume test.**

  In `useServerErrorPriorityUpdates.test.tsx`, add `pollingEnabled = true` to `Harness` and forward it to the hook. Then add this test after the current immediate-poll test:

  ```tsx
  it('preserves the acknowledged cursor while polling is paused, then resumes from it', async () => {
    mocks.listServerErrorPriorityDemotionUpdates
      .mockResolvedValueOnce({
        items: [UPDATE],
        nextCursor: { completedAtMs: UPDATE.completedAtMs, id: UPDATE.id },
      })
      .mockResolvedValueOnce({
        items: [],
        nextCursor: { completedAtMs: UPDATE.completedAtMs + 1, id: UPDATE.id + 1 },
      });

    await act(async () => {
      renderer = create(<Harness />);
      await flush();
    });
    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenCalledTimes(1);

    await act(async () => {
      renderer?.update(<Harness pollingEnabled={false} />);
      await vi.advanceTimersByTimeAsync(5_000);
      await flush();
    });
    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenCalledTimes(1);

    await act(async () => {
      renderer?.update(<Harness pollingEnabled />);
      await flush();
    });
    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenLastCalledWith(
      SCOPE.apiBase,
      SCOPE.managementKey,
      { completedAtMs: UPDATE.completedAtMs, id: UPDATE.id },
      expect.any(AbortSignal)
    );
  });
  ```

- [x] **Step 2: Write the failing scope-change test.**

  Allow `Harness` to accept `managerRequestScope`, then add a test which pauses at `{ completedAtMs: UPDATE.completedAtMs, id: UPDATE.id }`, changes only the Manager API base while still paused, and resumes. Assert the first resumed cursor has `id: 0` and does not equal `UPDATE.completedAtMs`; this proves credentials/scopes never inherit an old cursor.

- [x] **Step 3: Run the focused hook test and verify the new tests fail.**

  Run:

  ```powershell
  npm --workspace apps/web test -- useServerErrorPriorityUpdates.test.tsx
  ```

  Expected: the new test fails because the current hook clears `cursorRef.current` whenever its `enabled` state becomes false.

- [x] **Step 4: Add a separate `pollingEnabled` option and make cursor lifetime explicit.**

  In `useServerErrorPriorityUpdates.ts`, extend the options type:

  ```ts
  pollingEnabled?: boolean;
  ```

  Derive three distinct states, rather than overloading the old `enabled` condition:

  ```ts
  const transportReady =
    active && documentVisible && hasLoaded && Boolean(apiBase && managementKey);
  const enabled = transportReady && pollingEnabled !== false;
  const cursorScopeRef = useRef<string | null>(null);
  ```

  In the polling lifecycle effect, follow this exact order:

  ```ts
  generationRef.current += 1;
  const generation = generationRef.current;
  activeRequestRef.current?.controller.abort();
  activeRequestRef.current = null;

  const scopeChanged = cursorScopeRef.current !== scopeKey;
  if (scopeChanged) {
    cursorScopeRef.current = scopeKey;
    cursorRef.current = null;
  }
  if (!transportReady) {
    cursorRef.current = null;
    return;
  }
  if (!cursorRef.current) cursorRef.current = initialCursor();
  if (!enabled) return;

  void poll(generation);
  const timer = setInterval(() => void poll(generation), POLL_INTERVAL_MS);
  return () => clearInterval(timer);
  ```

  Keep the existing generation check, AbortController behavior, single-flight guard, five-second initial lookback, and tuple `normalizedCursor` validation. A user pause therefore aborts an unconfirmed request but retains the last **confirmed** cursor; a changed Manager scope or an inactive/hidden/unloaded page does not.

- [x] **Step 5: Run the focused hook test and commit the isolated hook change.**

  Run:

  ```powershell
  npm --workspace apps/web test -- useServerErrorPriorityUpdates.test.tsx
  git add apps/web/src/features/accounts/hooks/useServerErrorPriorityUpdates.ts apps/web/src/features/accounts/hooks/useServerErrorPriorityUpdates.test.tsx
  git commit -m "feat: preserve priority updates across list refresh pause"
  ```

  Expected: all hook tests pass, including existing abort/single-flight coverage.

## Task 2: Gate passive Accounts polling and retain explicit reads

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx`

- [x] **Step 1: Add failing Accounts-page tests for the off state.**

  In the existing fake-timer Accounts tests, add a helper that finds the new switch by `aria-label: 'accounts.live_refresh'`. Add these assertions:

  ```tsx
  const liveRefreshToggle = renderer.root
    .findAllByType(ToggleSwitch)
    .find((node) => node.props.ariaLabel === 'accounts.live_refresh');
  if (!liveRefreshToggle) throw new Error('Live refresh toggle not found');

  await act(async () => {
    liveRefreshToggle.props.onChange(false);
    await vi.advanceTimersByTimeAsync(120_000);
    await flushPromises();
  });

  expect(mocks.getAccountRecentIndex).toHaveBeenCalledTimes(initialRecentIndexCalls);
  expect(mocks.getAccountHistory).toHaveBeenCalledTimes(initialHistoryCalls);
  expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(initialWindowUsageCalls);
  expect(mocks.getHeaderSnapshots).toHaveBeenCalledTimes(initialHeaderCalls);
  expect(mocks.listCodexInspectionRuns).toHaveBeenCalledTimes(initialInspectionCalls);
  expect(mocks.lastServerErrorPriorityUpdatesOptions?.pollingEnabled).toBe(false);
  ```

  Add a second test that turns the switch back on while the sort is `recent`, flushes promises, and asserts exactly one new visible `recent_limit: 9` request plus one global request with `recent_limit === undefined`; it must also resume history/window/header reads once, without calling `refreshQuotaRows`, `accountQuotaSnapshotApi.write`, or a Token-recovery API.

- [x] **Step 2: Add the persisted state and a zero-fan-out freshness marker.**

  Import `useLocalStorage` and `formatTimestamp`. Near existing page state, add:

  ```ts
  const [liveRefreshEnabled, setLiveRefreshEnabled] = useLocalStorage<boolean>(
    'accountsPage.liveRefreshEnabled',
    true
  );
  const liveRefreshEnabledRef = useRef(liveRefreshEnabled);
  liveRefreshEnabledRef.current = liveRefreshEnabled;
  const latestListEvidenceAtMsRef = useRef<number | null>(null);
  const [frozenListEvidenceAtMs, setFrozenListEvidenceAtMs] = useState<number | null>(null);
  const accountsLivePollingEnabled =
    activeView === 'accounts' && documentVisible && liveRefreshEnabled;
  ```

  Add a stable callback which only causes a React render when the list is frozen:

  ```ts
  const recordListEvidenceRead = useCallback(() => {
    const observedAtMs = Date.now();
    latestListEvidenceAtMsRef.current = observedAtMs;
    if (!liveRefreshEnabledRef.current) setFrozenListEvidenceAtMs(observedAtMs);
  }, []);
  ```

  Call `recordListEvidenceRead()` only after a successful Manager read commits current data in `loadVisibleAccountRecentIndex`, `loadAccountHistory`, `loadListWindowUsage`, and the successful passive Header/inspection completion path. Do not store a running timestamp in React state: that would force a full 50-card rerender every ten seconds and defeat the performance work.

- [x] **Step 3: Add one toggle handler that freezes only browser display polling.**

  Implement:

  ```ts
  const setAccountsLiveRefresh = useCallback(
    (enabled: boolean) => {
      setLiveRefreshEnabled(enabled);
      if (!enabled) {
        setFrozenListEvidenceAtMs(latestListEvidenceAtMsRef.current);
        return;
      }
      setFrozenListEvidenceAtMs(null);
      setVisibleRecentIndexRefreshRevision((value) => value + 1);
      setAccountHistoryAutoRefreshRevision((value) => value + 1);
      setListWindowUsageRefreshRevision((value) => value + 1);
      if (accountSort.key === 'recent') {
        setGlobalRecentIndexRefreshRevision((value) => value + 1);
      }
      void refreshPassiveAccountsEvidence();
    },
    [accountSort.key, refreshPassiveAccountsEvidence, setLiveRefreshEnabled]
  );
  ```

  This handler must not call quota-refresh functions, Token Acquisition, credential mutation, Provider APIs, or any CPA write route.

- [x] **Step 4: Gate only passive reads; preserve explicit context and manual behavior.**

  Apply `accountsLivePollingEnabled` to:

  - passive Header/inspection `useInterval` and the visibility-regained passive-refresh effect;
  - the 10-second visible recent-index interval;
  - the 60-second history/window interval;
  - the 30-second global recent-index interval;
  - `useServerErrorPriorityUpdates({ pollingEnabled: liveRefreshEnabled, ... })`.

  Do **not** add the preference to context/load keys. Existing effects must still read a newly selected page, a newly chosen filter, a sort change, or the first non-empty page while frozen; that is an explicit user context change, not background refresh.

  In `refreshAccountsWorkspace`, increment `visibleRecentIndexRefreshRevision` together with the existing explicit history/global/window revisions. This makes the existing top-level manual `刷新` button pull a current-page nine-request snapshot even when the switch is off, without toggling it back on.

- [x] **Step 5: Run the Accounts tests to verify the intended failure is now fixed.**

  Run:

  ```powershell
  npm --workspace apps/web test -- AccountsPage.test.tsx
  ```

  Expected: timer suppression and resume tests pass; existing 10/30/60-second behavior remains unchanged when no stored preference exists.

- [x] **Step 6: Commit the polling-boundary behavior.**

  ```powershell
  git add apps/web/src/features/accounts/AccountsPage.tsx apps/web/src/features/accounts/AccountsPage.test.tsx
  git commit -m "feat: add accounts live polling gate"
  ```

## Task 3: Render the persisted control and frozen snapshot status

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`
- Modify: `apps/web/src/features/accounts/AccountsPage.module.scss`
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`
- Modify: `apps/web/src/i18n/locales/ru.json`

- [x] **Step 1: Write UI/persistence tests before rendering the control.**

  Add a test that renders the default page, finds the `ToggleSwitch` labelled `accounts.live_refresh`, and asserts `checked === true`. Toggle it off, then assert:

  ```tsx
  expect(window.localStorage.getItem('accountsPage.liveRefreshEnabled')).toBe('false');
  expect(treeText(renderer)).toContain('accounts.live_refresh_frozen');
  ```

  Unmount and render a second Accounts page in the same test; assert the switch remains false. Toggle it on and assert the stored value is `true` and the frozen badge is absent.

- [x] **Step 2: Add localized labels in all shipped locales.**

  In the `accounts` object of each locale, add exactly these keys:

  ```json
  "live_refresh": "列表实时刷新",
  "live_refresh_hint": "关闭后冻结凭证列表的自动数据读取；后台调度、计费和自动处理不受影响。",
  "live_refresh_frozen": "已冻结",
  "live_refresh_frozen_at": "已冻结 · 快照 {{time}}"
  ```

  Use faithful English, Traditional Chinese, and Russian equivalents in their corresponding files. Do not alter existing `refresh_quota`, `refresh_history`, or monitoring auto-refresh labels.

- [x] **Step 3: Render the control in the idle batch toolbar.**

  In `renderBatchBar`, when there is no selection and selection mode is off, render:

  ```tsx
  <div className={styles.liveRefreshControl}>
    <ToggleSwitch
      checked={liveRefreshEnabled}
      onChange={setAccountsLiveRefresh}
      ariaLabel={t('accounts.live_refresh')}
      label={t('accounts.live_refresh')}
    />
    {!liveRefreshEnabled ? (
      <span
        className={styles.liveRefreshFrozen}
        title={t('accounts.live_refresh_hint')}
      >
        {frozenListEvidenceAtMs
          ? t('accounts.live_refresh_frozen_at', {
              time: formatTimestamp(frozenListEvidenceAtMs, i18n.language, true),
            })
          : t('accounts.live_refresh_frozen')}
      </span>
    ) : null}
  </div>
  ```

  Keep the existing selection controls, proxy filter, current-page token update, banned deletion, quota refresh, and view-mode controls untouched. The new control controls *display reads only* and must never disable unrelated action buttons.

- [x] **Step 4: Add responsive styles without altering the batch toolbar layout contract.**

  Add `.liveRefreshControl` and `.liveRefreshFrozen` beside existing `.batchActions` rules:

  ```scss
  .liveRefreshControl {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }

  .liveRefreshFrozen {
    color: var(--color-warning-600);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  ```

  In the existing mobile `.batchActions` section, allow `.liveRefreshControl` to use full width and wrap its label rather than forcing horizontal overflow. Reuse only existing theme variables present in `AccountsPage.module.scss`.

- [x] **Step 5: Run focused UI/localization tests and commit.**

  Run:

  ```powershell
  npm --workspace apps/web test -- AccountsPage.test.tsx zh-CN.test.ts
  git add apps/web/src/features/accounts/AccountsPage.tsx apps/web/src/features/accounts/AccountsPage.module.scss apps/web/src/features/accounts/AccountsPage.test.tsx apps/web/src/i18n/locales/en.json apps/web/src/i18n/locales/zh-CN.json apps/web/src/i18n/locales/zh-TW.json apps/web/src/i18n/locales/ru.json
  git commit -m "feat: expose accounts live refresh toggle"
  ```

## Task 4: Record the custom behavior and verify the CPAM-only boundary

**Files:**
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`
- Modify: `docs/superpowers/plans/2026-10-22-accounts-live-refresh-toggle.md` (check off completed tasks during execution)

- [x] **Step 1: Add a durable memory entry.**

  Append a dated `2026-10-22` section stating:

  - preference key `accountsPage.liveRefreshEnabled`, default true, is browser-local;
  - off stops only front-end passive 10/30/60-second reads and priority-update projection;
  - initial/context/manual reads still work, and resume performs one bounded catch-up;
  - priority cursor must survive only a user pause and reset on scope/lifecycle invalidation;
  - no CPA Core, billing, Credits, auth-file, proxy, scheduler, quota mutation, or Token Acquisition behavior may be changed in a future merge.

- [x] **Step 2: Run the complete relevant verification suite.**

  Run:

  ```powershell
  npm --workspace apps/web test -- AccountsPage.test.tsx useServerErrorPriorityUpdates.test.tsx zh-CN.test.ts
  npm --workspace apps/web run type-check
  npm --workspace apps/web run lint
  npm --workspace apps/web run build
  $implementationBase = (git log --format=%H --grep='^docs: plan accounts live refresh toggle$' -1).Trim()
  git diff --check "$implementationBase..HEAD"
  git status --short
  ```

  Expected: all tests/type-check/build succeed; lint has no errors; diff check is clean; no file exists outside CPAM web/docs scope.

- [x] **Step 3: Inspect boundary-sensitive diff and commit memory.**

  Run:

  ```powershell
  $implementationBase = (git log --format=%H --grep='^docs: plan accounts live refresh toggle$' -1).Trim()
  git diff --stat "$implementationBase..HEAD"
  git diff "$implementationBase..HEAD" -- apps/web/src/features/accounts docs/LO-CPAM-CUSTOM-MEMORY.md
  git add docs/LO-CPAM-CUSTOM-MEMORY.md docs/superpowers/plans/2026-10-22-accounts-live-refresh-toggle.md
  git commit -m "docs: preserve accounts refresh toggle behavior"
  ```

  Confirm the diff contains no `apps/api`, CPA Core, `cli-proxy-api`, database schema, billing, quota-write, Token Acquisition, credential mutation, or deployment changes. Do not deploy or push in this task; deployment remains a separate explicit instruction.
