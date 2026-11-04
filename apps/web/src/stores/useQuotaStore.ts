/**
 * Quota cache that survives route switches.
 */

import { create } from 'zustand';
import { persist, type PersistStorage, type StorageValue } from 'zustand/middleware';
import type {
  AntigravityQuotaState,
  ClaudeQuotaState,
  CodexQuotaState,
  CredentialScopedQuotaState,
  DevinQuotaState,
  KimiQuotaState,
  MetaQuotaState,
  XaiQuotaState,
} from '@/types';
import { obfuscatedStorage } from '@/services/storage/secureStorage';
import { STORAGE_KEY_QUOTA_CACHE } from '@/utils/constants';

type QuotaUpdater<T> = T | ((prev: T) => T);

export const CODEX_QUOTA_MEMORY_ENTRY_LIMIT = 800;
export const CODEX_QUOTA_PERSISTED_ENTRY_LIMIT = 400;
export const OTHER_QUOTA_PERSISTED_ENTRY_LIMIT = 50;
export const QUOTA_CACHE_MAX_SERIALIZED_BYTES = Math.floor(1.25 * 1024 * 1024);

const QUOTA_CACHE_STORAGE_VERSION = 1;
const PERSISTED_QUOTA_ERROR_MAX_LENGTH = 512;
const QUOTA_CACHE_IDLE_TIMEOUT_MS = 500;
const QUOTA_CACHE_EMERGENCY_SERIALIZED_BYTES = Math.floor(256 * 1024);

interface QuotaStoreState {
  cacheScope: string;
  cacheGeneration: number;
  antigravityQuota: Record<string, AntigravityQuotaState>;
  claudeQuota: Record<string, ClaudeQuotaState>;
  codexQuota: Record<string, CodexQuotaState>;
  devinQuota: Record<string, DevinQuotaState>;
  kimiQuota: Record<string, KimiQuotaState>;
  metaQuota: Record<string, MetaQuotaState>;
  xaiQuota: Record<string, XaiQuotaState>;
  setAntigravityQuota: (updater: QuotaUpdater<Record<string, AntigravityQuotaState>>) => void;
  setClaudeQuota: (updater: QuotaUpdater<Record<string, ClaudeQuotaState>>) => void;
  setCodexQuota: (updater: QuotaUpdater<Record<string, CodexQuotaState>>) => void;
  setDevinQuota: (updater: QuotaUpdater<Record<string, DevinQuotaState>>) => void;
  setKimiQuota: (updater: QuotaUpdater<Record<string, KimiQuotaState>>) => void;
  setMetaQuota: (updater: QuotaUpdater<Record<string, MetaQuotaState>>) => void;
  setXaiQuota: (updater: QuotaUpdater<Record<string, XaiQuotaState>>) => void;
  activateQuotaCacheScope: (scope: string) => void;
  clearQuotaCache: () => void;
}

const resolveUpdater = <T>(updater: QuotaUpdater<T>, prev: T): T => {
  if (typeof updater === 'function') {
    return (updater as (value: T) => T)(prev);
  }
  return updater;
};

const emptyQuotaState = {
  antigravityQuota: {},
  claudeQuota: {},
  codexQuota: {},
  devinQuota: {},
  kimiQuota: {},
  metaQuota: {},
  xaiQuota: {},
};

type PersistedQuotaStoreState = Pick<
  QuotaStoreState,
  | 'cacheScope'
  | 'antigravityQuota'
  | 'claudeQuota'
  | 'codexQuota'
  | 'devinQuota'
  | 'kimiQuota'
  | 'metaQuota'
  | 'xaiQuota'
>;

const quotaCacheProviderKeys = [
  'antigravityQuota',
  'claudeQuota',
  'codexQuota',
  'devinQuota',
  'kimiQuota',
  'metaQuota',
  'xaiQuota',
] as const satisfies ReadonlyArray<Exclude<keyof PersistedQuotaStoreState, 'cacheScope'>>;

type PersistableQuotaState = CredentialScopedQuotaState & {
  status?: string;
  observedFromUsageHeaders?: boolean;
};

const isPersistableQuotaState = (
  item: PersistableQuotaState | undefined
): item is PersistableQuotaState & { authFileKey: string } =>
  Boolean(
    item?.authFileKey?.trim() &&
      item.authFileIdentityVerified === true &&
      (item.status === 'success' || item.status === 'error')
  );

const finiteTimestamp = (value: unknown): number =>
  typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : 0;

const quotaStateRecency = (item: CredentialScopedQuotaState): number => {
  const observed = item as CredentialScopedQuotaState & {
    observedAtMs?: unknown;
    resetCreditsEvidenceAtMs?: unknown;
  };
  return Math.max(
    finiteTimestamp(item.fetchedAtMs),
    finiteTimestamp(item.failedAtMs),
    finiteTimestamp(observed.observedAtMs),
    finiteTimestamp(observed.resetCreditsEvidenceAtMs)
  );
};

const keepNewestQuotaRecords = <TState extends CredentialScopedQuotaState>(
  quota: Record<string, TState>,
  limit: number
): Record<string, TState> => {
  const entries = Object.entries(quota);
  if (entries.length <= limit) return quota;

  return Object.fromEntries(
    entries
      .sort(([leftKey, left], [rightKey, right]) => {
        const recencyDelta = quotaStateRecency(right) - quotaStateRecency(left);
        return recencyDelta || leftKey.localeCompare(rightKey);
      })
      .slice(0, limit)
  );
};

const filterPersistableQuotaStates = <TState extends PersistableQuotaState>(
  quota: Record<string, TState> | undefined
): Record<string, TState> => {
  if (!quota) return {};

  const persisted = Object.fromEntries(
    Object.values(quota)
      .filter(isPersistableQuotaState)
      .map((item) => [item.authFileKey, item])
  );

  return keepNewestQuotaRecords(persisted, OTHER_QUOTA_PERSISTED_ENTRY_LIMIT);
};

const quotaStateForScope = (cacheScope: string, cacheGeneration: number) => ({
  cacheScope,
  cacheGeneration,
  ...emptyQuotaState,
});

const normalizePersistedCodexQuotaState = (
  item: CodexQuotaState & { authFileKey: string }
): CodexQuotaState & { authFileKey: string } => {
  const rawLimit = (
    item as unknown as {
      spendControlIndividualLimit?: unknown;
    }
  ).spendControlIndividualLimit;
  if (typeof rawLimit !== 'number' && typeof rawLimit !== 'string') return item;

  const limit = String(rawLimit).trim();
  if (!limit || !Number.isFinite(Number(limit))) {
    return { ...item, spendControlIndividualLimit: null };
  }

  return {
    ...item,
    spendControlIndividualLimit: {
      source: null,
      unit: null,
      limit,
      used: null,
      remaining: null,
      usedPercent: null,
      remainingPercent: null,
      resetAfterSeconds: null,
      resetAtMs: null,
    },
  };
};

const truncatePersistedDiagnostic = (value: string | undefined): string | undefined => {
  if (typeof value !== 'string' || value.length <= PERSISTED_QUOTA_ERROR_MAX_LENGTH) return value;
  return value.slice(0, PERSISTED_QUOTA_ERROR_MAX_LENGTH);
};

const truncateNullablePersistedDiagnostic = (value: string | null | undefined): string | null | undefined =>
  value === null ? null : truncatePersistedDiagnostic(value);

const compactPersistedCodexQuotaState = (
  item: CodexQuotaState & { authFileKey: string }
): CodexQuotaState & { authFileKey: string } => {
  const normalized = normalizePersistedCodexQuotaState(item);
  const withoutResetCredits = { ...normalized };
  delete withoutResetCredits.rateLimitResetCredits;
  return {
    ...withoutResetCredits,
    error: truncatePersistedDiagnostic(withoutResetCredits.error),
    rateLimitResetCreditsError: truncateNullablePersistedDiagnostic(
      withoutResetCredits.rateLimitResetCreditsError
    ),
  };
};

const filterPersistableCodexQuota = (
  quota: Record<string, CodexQuotaState> | undefined
): Record<string, CodexQuotaState> => {
  if (!quota) return {};

  const persisted = Object.fromEntries(
    Object.values(quota)
      .filter(
        (item): item is CodexQuotaState & { authFileKey: string } =>
          isPersistableQuotaState(item) &&
          (item.status !== 'success' || item.observedFromUsageHeaders !== true)
      )
      .map((item) => {
        const compact = compactPersistedCodexQuotaState(item);
        return [compact.authFileKey, compact];
      })
  );

  return keepNewestQuotaRecords(persisted, CODEX_QUOTA_PERSISTED_ENTRY_LIMIT);
};

const clonePersistedQuotaState = (
  state: PersistedQuotaStoreState
): PersistedQuotaStoreState => ({
  cacheScope: state.cacheScope,
  antigravityQuota: { ...state.antigravityQuota },
  claudeQuota: { ...state.claudeQuota },
  codexQuota: { ...state.codexQuota },
  devinQuota: { ...state.devinQuota },
  kimiQuota: { ...state.kimiQuota },
  metaQuota: { ...state.metaQuota },
  xaiQuota: { ...state.xaiQuota },
});

const persistedQuotaCacheByteLength = (state: PersistedQuotaStoreState): number => {
  const serialized = JSON.stringify({ state, version: QUOTA_CACHE_STORAGE_VERSION });
  return new TextEncoder().encode(serialized).byteLength;
};

const oldestPersistedQuotaEntries = (state: PersistedQuotaStoreState) =>
  quotaCacheProviderKeys
    .flatMap((provider) =>
      Object.entries(state[provider]).map(([key, item]) => ({
        provider,
        key,
        recency: quotaStateRecency(item),
      }))
    )
    .sort(
      (left, right) =>
        left.recency - right.recency ||
        left.provider.localeCompare(right.provider) ||
        left.key.localeCompare(right.key)
    );

const removeOldestPersistedQuotaEntries = (
  state: PersistedQuotaStoreState,
  entries: ReturnType<typeof oldestPersistedQuotaEntries>,
  removeCount: number
): PersistedQuotaStoreState => {
  const next = clonePersistedQuotaState(state);
  for (let index = 0; index < removeCount; index += 1) {
    const entry = entries[index];
    if (!entry) break;
    delete next[entry.provider][entry.key];
  }
  return next;
};

const fitPersistedQuotaCacheByteBudget = (
  state: PersistedQuotaStoreState,
  byteBudget = QUOTA_CACHE_MAX_SERIALIZED_BYTES
): PersistedQuotaStoreState => {
  if (persistedQuotaCacheByteLength(state) <= byteBudget) return state;

  const entries = oldestPersistedQuotaEntries(state);
  let low = 0;
  let high = entries.length;

  while (low < high) {
    const middle = Math.floor((low + high) / 2);
    const candidate = removeOldestPersistedQuotaEntries(state, entries, middle + 1);
    if (persistedQuotaCacheByteLength(candidate) <= byteBudget) {
      high = middle;
    } else {
      low = middle + 1;
    }
  }

  return removeOldestPersistedQuotaEntries(state, entries, low + 1);
};

const compactPersistedQuotaState = (
  state: Partial<QuotaStoreState> | undefined
): PersistedQuotaStoreState =>
  fitPersistedQuotaCacheByteBudget({
    cacheScope: typeof state?.cacheScope === 'string' ? state.cacheScope : '',
    antigravityQuota: filterPersistableQuotaStates(state?.antigravityQuota),
    claudeQuota: filterPersistableQuotaStates(state?.claudeQuota),
    codexQuota: filterPersistableCodexQuota(state?.codexQuota),
    devinQuota: filterPersistableQuotaStates(state?.devinQuota),
    kimiQuota: filterPersistableQuotaStates(state?.kimiQuota),
    metaQuota: filterPersistableQuotaStates(state?.metaQuota),
    xaiQuota: filterPersistableQuotaStates(state?.xaiQuota),
  });

const partializeQuotaStoreState = (state: QuotaStoreState): PersistedQuotaStoreState =>
  compactPersistedQuotaState(state);

type PendingQuotaCacheWrite = {
  name: string;
  value: StorageValue<PersistedQuotaStoreState>;
};

let pendingQuotaCacheWrite: PendingQuotaCacheWrite | null = null;
type PendingQuotaCacheSchedule =
  | { kind: 'idle'; handle: number }
  | { kind: 'microtask' };

let pendingQuotaCacheSchedule: PendingQuotaCacheSchedule | null = null;
let quotaCacheWriteFailureReported = false;

type IdleCallbackScheduler = {
  requestIdleCallback?: (callback: () => void, options?: { timeout?: number }) => number;
  cancelIdleCallback?: (handle: number) => void;
};

const idleCallbackScheduler = globalThis as typeof globalThis & IdleCallbackScheduler;

const cancelPendingQuotaCacheSchedule = (): void => {
  if (pendingQuotaCacheSchedule?.kind === 'idle') {
    idleCallbackScheduler.cancelIdleCallback?.(pendingQuotaCacheSchedule.handle);
  }
  pendingQuotaCacheSchedule = null;
};

const writeQuotaCacheSafely = (pending: PendingQuotaCacheWrite): void => {
  const compactValue: StorageValue<PersistedQuotaStoreState> = {
    ...pending.value,
    state: compactPersistedQuotaState(pending.value.state),
  };

  try {
    obfuscatedStorage.setItem(pending.name, compactValue);
    quotaCacheWriteFailureReported = false;
    return;
  } catch (error) {
    const emergencyValue: StorageValue<PersistedQuotaStoreState> = {
      ...compactValue,
      state: fitPersistedQuotaCacheByteBudget(
        compactValue.state,
        QUOTA_CACHE_EMERGENCY_SERIALIZED_BYTES
      ),
    };

    try {
      obfuscatedStorage.setItem(pending.name, emergencyValue);
      quotaCacheWriteFailureReported = false;
      return;
    } catch (emergencyError) {
      try {
        obfuscatedStorage.removeItem(pending.name);
      } catch {
        // Browser storage remains unavailable. Live quota state must stay usable.
      }

      if (!quotaCacheWriteFailureReported) {
        quotaCacheWriteFailureReported = true;
        console.warn('Quota cache persistence skipped because browser storage is full.', {
          error,
          emergencyError,
        });
      }
    }
  }
};

export const flushPendingQuotaCacheWrites = (): void => {
  cancelPendingQuotaCacheSchedule();

  const pending = pendingQuotaCacheWrite;
  pendingQuotaCacheWrite = null;
  if (!pending || typeof localStorage === 'undefined') return;

  writeQuotaCacheSafely(pending);
};

const quotaCacheStorage: PersistStorage<PersistedQuotaStoreState> = {
  getItem: (name) => {
    if (typeof localStorage === 'undefined') return null;
    return obfuscatedStorage.getItem<StorageValue<PersistedQuotaStoreState>>(name);
  },
  setItem: (name, value) => {
    if (typeof localStorage === 'undefined') return;

    pendingQuotaCacheWrite = { name, value };
    if (pendingQuotaCacheSchedule !== null) return;

    if (typeof idleCallbackScheduler.requestIdleCallback === 'function') {
      const handle = idleCallbackScheduler.requestIdleCallback(
        () => {
          pendingQuotaCacheSchedule = null;
          flushPendingQuotaCacheWrites();
        },
        { timeout: QUOTA_CACHE_IDLE_TIMEOUT_MS }
      );
      pendingQuotaCacheSchedule = { kind: 'idle', handle };
      return;
    }

    pendingQuotaCacheSchedule = { kind: 'microtask' };
    queueMicrotask(() => {
      if (pendingQuotaCacheSchedule?.kind !== 'microtask') return;
      pendingQuotaCacheSchedule = null;
      flushPendingQuotaCacheWrites();
    });
  },
  removeItem: (name) => {
    if (pendingQuotaCacheWrite?.name === name) {
      pendingQuotaCacheWrite = null;
    }
    cancelPendingQuotaCacheSchedule();
    if (typeof localStorage === 'undefined') return;

    try {
      obfuscatedStorage.removeItem(name);
    } catch {
      // Storage cleanup must not make account quota actions fail.
    }
  },
};

if (typeof window !== 'undefined') {
  window.addEventListener('pagehide', flushPendingQuotaCacheWrites);
}

export const useQuotaStore = create<QuotaStoreState>()(
  persist(
    (set) => ({
      cacheScope: '',
      cacheGeneration: 0,
      ...emptyQuotaState,
      setAntigravityQuota: (updater) =>
        set((state) => ({
          antigravityQuota: resolveUpdater(updater, state.antigravityQuota),
        })),
      setClaudeQuota: (updater) =>
        set((state) => ({
          claudeQuota: resolveUpdater(updater, state.claudeQuota),
        })),
      setCodexQuota: (updater) =>
        set((state) => ({
          codexQuota: keepNewestQuotaRecords(
            resolveUpdater(updater, state.codexQuota),
            CODEX_QUOTA_MEMORY_ENTRY_LIMIT
          ),
        })),
      setDevinQuota: (updater) =>
        set((state) => ({
          devinQuota: resolveUpdater(updater, state.devinQuota),
        })),
      setKimiQuota: (updater) =>
        set((state) => ({
          kimiQuota: resolveUpdater(updater, state.kimiQuota),
        })),
      setMetaQuota: (updater) =>
        set((state) => ({
          metaQuota: resolveUpdater(updater, state.metaQuota),
        })),
      setXaiQuota: (updater) =>
        set((state) => ({
          xaiQuota: resolveUpdater(updater, state.xaiQuota),
        })),
      activateQuotaCacheScope: (scope) =>
        set((state) => {
          const nextScope = scope.trim();
          if (state.cacheScope === nextScope) return state;
          return quotaStateForScope(nextScope, state.cacheGeneration + 1);
        }),
      clearQuotaCache: () => set((state) => quotaStateForScope('', state.cacheGeneration + 1)),
    }),
    {
      name: STORAGE_KEY_QUOTA_CACHE,
      storage: quotaCacheStorage,
      version: QUOTA_CACHE_STORAGE_VERSION,
      partialize: partializeQuotaStoreState,
      migrate: (persistedState) =>
        compactPersistedQuotaState(persistedState as Partial<QuotaStoreState> | undefined),
      merge: (persistedState, currentState) => {
        const persisted = compactPersistedQuotaState(
          persistedState as Partial<QuotaStoreState> | undefined
        );
        return {
          ...currentState,
          cacheScope: persisted.cacheScope,
          antigravityQuota: persisted.antigravityQuota,
          claudeQuota: persisted.claudeQuota,
          codexQuota: keepNewestQuotaRecords(
            persisted.codexQuota,
            CODEX_QUOTA_MEMORY_ENTRY_LIMIT
          ),
          devinQuota: persisted.devinQuota,
          kimiQuota: persisted.kimiQuota,
          metaQuota: persisted.metaQuota,
          xaiQuota: persisted.xaiQuota,
        };
      },
    }
  )
);

export const captureQuotaCacheGeneration = (): number => useQuotaStore.getState().cacheGeneration;

export const isQuotaCacheGenerationCurrent = (generation: number): boolean =>
  useQuotaStore.getState().cacheGeneration === generation;

export const commitIfQuotaCacheCurrent = (generation: number, commit: () => void): boolean => {
  if (useQuotaStore.getState().cacheGeneration !== generation) return false;
  commit();
  return true;
};
