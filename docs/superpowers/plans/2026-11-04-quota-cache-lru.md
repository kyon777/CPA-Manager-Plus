# Quota Cache LRU Guard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep browser quota display cache bounded for large Codex fleets without allowing `localStorage` quota errors to surface as credential failures.

**Architecture:** Keep the live Codex store bounded to the 800 most recently refreshed credential records. Persist only a compact, identity-verified subset: newest 400 Codex records plus a 1.25 MiB serialized envelope budget across all quota providers. Replace synchronous `createJSONStorage` writes with a debounced object storage adapter that coalesces refresh bursts, migrates version-0 oversized entries, and catches quota errors before they reach quota refresh callers.

**Tech Stack:** React 19, TypeScript, Zustand persist v5, Vitest, browser localStorage.

---

### Task 1: Add regression coverage for bounded quota persistence

**Files:**
- Modify: `apps/web/src/stores/useQuotaStore.test.ts`
- Test: `apps/web/src/stores/useQuotaStore.test.ts`

- [ ] **Step 1: Write failing tests for the three externally visible guarantees**

Add test fixtures that create identity-verified Codex states with deterministic `fetchedAtMs` values, then assert:

```ts
it('keeps the 800 most recently refreshed Codex states in memory', async () => {
  useQuotaStore.getState().setCodexQuota(makeCodexRecords(805));
  expect(Object.keys(useQuotaStore.getState().codexQuota)).toHaveLength(800);
  expect(useQuotaStore.getState().codexQuota['codex-804']).toBeDefined();
  expect(useQuotaStore.getState().codexQuota['codex-000']).toBeUndefined();
});

it('persists only the newest 400 compact Codex states', async () => {
  useQuotaStore.getState().setCodexQuota(makeCodexRecords(405, { resetCreditPayload: true }));
  flushPendingQuotaCacheWrites();
  const persisted = await readPersistedQuotaState();
  expect(Object.keys(persisted.codexQuota ?? {})).toHaveLength(400);
  expect(persisted.codexQuota?.['codex-404']?.rateLimitResetCredits).toBeUndefined();
});

it('keeps the stored quota envelope within the byte budget', async () => {
  useQuotaStore.getState().setCodexQuota(makeLargeCodexRecords());
  flushPendingQuotaCacheWrites();
  expect(readRawQuotaCacheLength()).toBeLessThanOrEqual(QUOTA_CACHE_MAX_SERIALIZED_BYTES);
});
```

- [ ] **Step 2: Run the focused test file and verify the new cases fail because no LRU/budget logic exists**

Run: `npm --workspace apps/web run test -- src/stores/useQuotaStore.test.ts`

Expected: FAIL assertions show more than 800 in memory, more than 400 persisted, or no exported byte-limit constant.

- [ ] **Step 3: Add the minimum helper assertions required by existing delayed persistence tests**

Change `readPersistedQuotaState` and `readPersistedQuotaScope` to flush the quota cache writer before reading localStorage. Do not alter product code in this step.

- [ ] **Step 4: Run the focused test file and retain the expected RED failures**

Run: `npm --workspace apps/web run test -- src/stores/useQuotaStore.test.ts`

Expected: Existing persistence tests remain meaningful; only new bounded-cache expectations fail.

### Task 2: Implement deterministic live and persisted LRU compaction

**Files:**
- Modify: `apps/web/src/stores/useQuotaStore.ts`
- Test: `apps/web/src/stores/useQuotaStore.test.ts`

- [ ] **Step 1: Write failing migration coverage for an existing oversized version-0 cache**

Seed `STORAGE_KEY_QUOTA_CACHE` with 405 verified Codex records, reset the module, and assert hydration keeps the newest persisted subset and queues a version-1 compact rewrite.

```ts
expect(Object.keys(hydratedQuotaStore.getState().codexQuota)).toHaveLength(400);
expect(Object.keys(await readPersistedQuotaState())).toHaveLength(400);
```

- [ ] **Step 2: Run the focused test and verify hydration does not yet migrate/rewrite**

Run: `npm --workspace apps/web run test -- src/stores/useQuotaStore.test.ts`

Expected: FAIL because current storage version remains zero and hydration keeps all old verified records.

- [ ] **Step 3: Implement quota record ordering and compact persisted Codex projection**

In `useQuotaStore.ts`:

```ts
export const CODEX_QUOTA_MEMORY_ENTRY_LIMIT = 800;
export const CODEX_QUOTA_PERSISTED_ENTRY_LIMIT = 400;
export const QUOTA_CACHE_MAX_SERIALIZED_BYTES = 1.25 * 1024 * 1024;
```

Use `fetchedAtMs`, then `failedAtMs`, then `observedAtMs` as refresh recency; break ties by canonical `authFileKey`. Keep newest records deterministically. The persisted Codex projection must retain quota windows and scalar status data but omit `rateLimitResetCredits` and truncate persisted diagnostic strings to a fixed safe length. Preserve full state in memory for currently retained records.

- [ ] **Step 4: Apply the 800-record live cap at every `setCodexQuota` boundary and the 400-record cap in `partialize`, `merge`, and migration**

Keep canonical identity filtering and legacy spend-control normalization intact. Add `version: 1` plus a `migrate` implementation so existing version-0 browser caches are compacted and rewritten after hydration.

- [ ] **Step 5: Run the focused tests and verify the LRU and migration cases pass**

Run: `npm --workspace apps/web run test -- src/stores/useQuotaStore.test.ts`

Expected: PASS.

### Task 3: Add a debounced, quota-fault-contained persistence adapter

**Files:**
- Modify: `apps/web/src/stores/useQuotaStore.ts`
- Modify: `apps/web/src/stores/useQuotaStore.test.ts`
- Test: `apps/web/src/stores/useQuotaStore.test.ts`

- [ ] **Step 1: Write failing tests for coalescing and quota-exception containment**

Use a storage double that counts `setItem` and another that throws a DOM-like `QuotaExceededError`. Assert:

```ts
useQuotaStore.getState().setCodexQuota(firstUpdate);
useQuotaStore.getState().setCodexQuota(secondUpdate);
expect(storage.setItem).not.toHaveBeenCalled();
flushPendingQuotaCacheWrites();
expect(storage.setItem).toHaveBeenCalledTimes(1);

expect(() => flushPendingQuotaCacheWrites()).not.toThrow();
expect(useQuotaStore.getState().codexQuota['current']).toBeDefined();
```

- [ ] **Step 2: Run the focused test and verify it fails with synchronous writes / escaping quota errors**

Run: `npm --workspace apps/web run test -- src/stores/useQuotaStore.test.ts`

Expected: FAIL because current `createJSONStorage` writes immediately and propagates storage exceptions.

- [ ] **Step 3: Replace `createJSONStorage` with a typed quota-cache `PersistStorage` adapter**

The adapter must:
1. hold only the latest pending `StorageValue`;
2. flush after a short debounce;
3. expose `flushPendingQuotaCacheWrites()` for page lifecycle flushing and deterministic tests;
4. compact one final time before write;
5. catch browser quota errors, retry a smaller emergency compact cache, and if that still fails remove only `STORAGE_KEY_QUOTA_CACHE`;
6. never rethrow a browser storage fault into the caller that updated the quota state.

Keep encrypted/obfuscated storage compatibility by continuing to read/write through `obfuscatedStorage`.

- [ ] **Step 4: Apply the global 1.25 MiB envelope cap before all durable writes**

Measure the actual JSON envelope bytes with `TextEncoder` before obfuscation. When over budget, remove globally oldest persisted quota records until under the limit, always retaining the cache scope and newest items. This protects non-Codex providers too without changing their live in-memory state.

- [ ] **Step 5: Run the focused tests and verify all persistence behavior passes**

Run: `npm --workspace apps/web run test -- src/stores/useQuotaStore.test.ts`

Expected: PASS.

### Task 4: Validate production build and inspect the final diff

**Files:**
- Modify: `apps/web/src/stores/useQuotaStore.ts`
- Modify: `apps/web/src/stores/useQuotaStore.test.ts`

- [ ] **Step 1: Run focused quota-store regression tests**

Run: `npm --workspace apps/web run test -- src/stores/useQuotaStore.test.ts`

Expected: PASS, including LRU, migration, byte budget, coalescing, and quota-exception containment.

- [ ] **Step 2: Run type checking and the frontend production build**

Run: `npm run type-check && npm run build`

Expected: both commands exit 0.

- [ ] **Step 3: Run the full web test suite and record any pre-existing failures separately**

Run: `npm run test:web`

Expected: quota-store tests remain green. If unrelated date-sensitive demo tests fail, record the exact file and do not attribute it to this change.

- [ ] **Step 4: Inspect only the intended diff**

Run: `git diff --check && git diff -- apps/web/src/stores/useQuotaStore.ts apps/web/src/stores/useQuotaStore.test.ts`

Expected: no whitespace errors; no CPA Core, server, billing, or backend files changed.

- [ ] **Step 5: Commit the bounded-cache implementation**

```bash
git add apps/web/src/stores/useQuotaStore.ts apps/web/src/stores/useQuotaStore.test.ts docs/superpowers/plans/2026-11-04-quota-cache-lru.md
git commit -m "fix(web): bound quota cache persistence"
```
