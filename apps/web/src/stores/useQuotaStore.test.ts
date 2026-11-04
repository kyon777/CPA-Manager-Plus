import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  AntigravityQuotaState,
  ClaudeQuotaState,
  CodexQuotaState,
  DevinQuotaState,
  KimiQuotaState,
  MetaQuotaState,
  XaiQuotaState,
} from '@/types';

type StorageLike = {
  getItem: (key: string) => string | null;
  setItem: (key: string, value: string) => void;
  removeItem: (key: string) => void;
  clear: () => void;
};

const createMemoryStorage = (): StorageLike => {
  const store = new Map<string, string>();
  return {
    getItem: (key) => (store.has(key) ? (store.get(key) as string) : null),
    setItem: (key, value) => {
      store.set(key, value);
    },
    removeItem: (key) => {
      store.delete(key);
    },
    clear: () => {
      store.clear();
    },
  };
};

const createTrackingStorage = () => {
  const storage = createMemoryStorage();
  return {
    ...storage,
    setItem: vi.fn(storage.setItem),
  };
};

const createQuotaExhaustedStorage = (): StorageLike => ({
  getItem: () => null,
  setItem: () => {
    const error = new Error('storage quota exhausted');
    error.name = 'QuotaExceededError';
    throw error;
  },
  removeItem: () => undefined,
  clear: () => undefined,
});

const QUOTA_CACHE_EXPECTED_MAX_SERIALIZED_BYTES = Math.floor(1.25 * 1024 * 1024);

const makeCodexQuotaRecords = (
  count: number,
  options: { windowLabelBytes?: number; includeResetCredits?: boolean } = {}
): Record<string, CodexQuotaState> =>
  Object.fromEntries(
    Array.from({ length: count }, (_, index) => {
      const authFileKey = `codex-${String(index).padStart(4, '0')}`;
      return [
        authFileKey,
        {
          status: 'success',
          windows: options.windowLabelBytes
            ? [
                {
                  id: 'monthly',
                  label: 'x'.repeat(options.windowLabelBytes),
                  usedPercent: 10,
                  resetLabel: 'tomorrow',
                },
              ]
            : [],
          authFileKey,
          authFileIdentityVerified: true,
          fetchedAtMs: index,
          ...(options.includeResetCredits
            ? {
                rateLimitResetCreditsAvailableCount: 1,
                rateLimitResetCredits: [
                  {
                    id: `${authFileKey}-credit`,
                    status: 'available',
                    grantedAt: '2026-11-04T00:00:00.000Z',
                    expiresAt: '2026-12-04T00:00:00.000Z',
                  },
                ],
              }
            : {}),
        } satisfies CodexQuotaState,
      ];
    })
  );

const makeClaudeQuotaRecords = (count: number): Record<string, ClaudeQuotaState> =>
  Object.fromEntries(
    Array.from({ length: count }, (_, index) => {
      const authFileKey = `claude-${String(index).padStart(4, '0')}`;
      return [
        authFileKey,
        {
          status: 'success',
          windows: [],
          authFileKey,
          authFileIdentityVerified: true,
          fetchedAtMs: index,
        } satisfies ClaudeQuotaState,
      ];
    })
  );

const readPersistedQuotaState = async () => {
  const { flushPendingQuotaCacheWrites } = await import('./useQuotaStore');
  const { STORAGE_KEY_QUOTA_CACHE } = await import('@/utils/constants');
  const { obfuscatedStorage } = await import('@/services/storage/secureStorage');
  flushPendingQuotaCacheWrites();
  const persisted = obfuscatedStorage.getItem<{
    state?: {
      antigravityQuota?: Record<string, AntigravityQuotaState>;
      claudeQuota?: Record<string, ClaudeQuotaState>;
      codexQuota?: Record<string, CodexQuotaState>;
      devinQuota?: Record<string, DevinQuotaState>;
      kimiQuota?: Record<string, KimiQuotaState>;
      metaQuota?: Record<string, MetaQuotaState>;
      xaiQuota?: Record<string, XaiQuotaState>;
    };
  }>(STORAGE_KEY_QUOTA_CACHE);
  return persisted?.state ?? {};
};

const readPersistedQuotaScope = async () => {
  const { flushPendingQuotaCacheWrites } = await import('./useQuotaStore');
  const { STORAGE_KEY_QUOTA_CACHE } = await import('@/utils/constants');
  const { obfuscatedStorage } = await import('@/services/storage/secureStorage');
  flushPendingQuotaCacheWrites();
  const persisted = obfuscatedStorage.getItem<{
    state?: { cacheScope?: string };
  }>(STORAGE_KEY_QUOTA_CACHE);
  return persisted?.state?.cacheScope ?? '';
};

describe('useQuotaStore persistence', () => {
  let storage: StorageLike;

  beforeEach(() => {
    vi.resetModules();
    storage = createMemoryStorage();
    vi.stubGlobal('localStorage', storage);
    vi.stubGlobal('window', {
      location: { host: 'cpa.test:8317' },
      addEventListener: vi.fn(),
    });
    vi.stubGlobal('navigator', { userAgent: 'quota-store-test' });
  });

  afterEach(async () => {
    const { flushPendingQuotaCacheWrites } = await import('./useQuotaStore');
    flushPendingQuotaCacheWrites();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('persists manually fetched Codex success and error states', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setCodexQuota({
      manual: {
        status: 'success',
        windows: [],
        authFileKey: 'manual',
        authFileIdentityVerified: true,
        fetchedAtMs: 2_000,
      },
      observed: {
        status: 'success',
        windows: [],
        authFileKey: 'observed',
        authFileIdentityVerified: true,
        observedFromUsageHeaders: true,
        observedAtMs: 1_000,
      },
      failed: {
        status: 'error',
        windows: [],
        error: 'failed',
        errorStatus: 401,
        authFileKey: 'failed',
        authFileIdentityVerified: true,
      },
      loading: {
        status: 'loading',
        windows: [],
      },
    });

    const persisted = await readPersistedQuotaState();
    expect(Object.keys(persisted.codexQuota ?? {})).toEqual(['manual', 'failed']);
    expect(persisted.codexQuota?.failed).toMatchObject({
      status: 'error',
      error: 'failed',
      errorStatus: 401,
    });
  });

  it('persists success and error states for every quota provider', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setClaudeQuota({
      claudeSuccess: {
        status: 'success',
        windows: [],
        authFileKey: 'claudeSuccess',
        authFileIdentityVerified: true,
      },
      claudeError: {
        status: 'error',
        windows: [],
        error: 'claude failed',
        errorStatus: 500,
        authFileKey: 'claudeError',
        authFileIdentityVerified: true,
      },
      claudeLoading: { status: 'loading', windows: [] },
    });
    useQuotaStore.getState().setAntigravityQuota({
      antigravitySuccess: {
        status: 'success',
        groups: [],
        authFileKey: 'antigravitySuccess',
        authFileIdentityVerified: true,
      },
      antigravityError: {
        status: 'error',
        groups: [],
        error: 'antigravity failed',
        authFileKey: 'antigravityError',
        authFileIdentityVerified: true,
      },
      antigravityLoading: { status: 'loading', groups: [] },
    });
    useQuotaStore.getState().setKimiQuota({
      kimiSuccess: {
        status: 'success',
        rows: [],
        authFileKey: 'kimiSuccess',
        authFileIdentityVerified: true,
      },
      kimiError: {
        status: 'error',
        rows: [],
        error: 'kimi failed',
        authFileKey: 'kimiError',
        authFileIdentityVerified: true,
      },
      kimiLoading: { status: 'loading', rows: [] },
    });
    useQuotaStore.getState().setXaiQuota({
      xaiSuccess: {
        status: 'success',
        billing: null,
        authFileKey: 'xaiSuccess',
        authFileIdentityVerified: true,
      },
      xaiError: {
        status: 'error',
        billing: null,
        error: 'xai failed',
        authFileKey: 'xaiError',
        authFileIdentityVerified: true,
      },
      xaiLoading: { status: 'loading', billing: null },
    });
    useQuotaStore.getState().setDevinQuota({
      devinSuccess: {
        status: 'success',
        windows: [],
        observedAtMs: null,
        plan: null,
        planStartMs: null,
        planEndMs: null,
        authFileKey: 'devinSuccess',
        authFileIdentityVerified: true,
      },
      devinError: {
        status: 'error',
        windows: [],
        observedAtMs: null,
        plan: null,
        planStartMs: null,
        planEndMs: null,
        error: 'devin failed',
        errorStatus: 500,
        authFileKey: 'devinError',
        authFileIdentityVerified: true,
      },
      devinLoading: {
        status: 'loading',
        windows: [],
        observedAtMs: null,
        plan: null,
        planStartMs: null,
        planEndMs: null,
      },
    });
    useQuotaStore.getState().setMetaQuota({
      metaSuccess: {
        status: 'success',
        windows: [
          {
            id: 'window',
            usedPercent: 30,
            resetAtMs: 1774000000000,
            resetAccuracy: 'exact',
            limitWindowSeconds: 18000,
            quotaProgressObservedAtMs: 1773000000000,
          },
        ],
        observedAtMs: 1773000000000,
        plan: 'Meta Pro',
        isSubscriptionActive: true,
        quotaInventoryObserved: true,
        authFileKey: 'metaSuccess',
        authFileIdentityVerified: true,
      },
      metaError: {
        status: 'error',
        windows: [],
        observedAtMs: 1773000000000,
        plan: null,
        isSubscriptionActive: null,
        quotaInventoryObserved: false,
        error: 'meta failed',
        errorStatus: 401,
        authFileKey: 'metaError',
        authFileIdentityVerified: true,
      },
      metaLoading: {
        status: 'loading',
        windows: [],
        observedAtMs: 1773000000000,
        plan: null,
        isSubscriptionActive: null,
        quotaInventoryObserved: false,
      },
    });

    const persisted = await readPersistedQuotaState();

    expect(Object.keys(persisted.claudeQuota ?? {})).toEqual(['claudeSuccess', 'claudeError']);
    expect(Object.keys(persisted.antigravityQuota ?? {})).toEqual([
      'antigravitySuccess',
      'antigravityError',
    ]);
    expect(Object.keys(persisted.kimiQuota ?? {})).toEqual(['kimiSuccess', 'kimiError']);
    expect(Object.keys(persisted.xaiQuota ?? {})).toEqual(['xaiSuccess', 'xaiError']);
    expect(Object.keys(persisted.devinQuota ?? {})).toEqual(['devinSuccess', 'devinError']);
    expect(Object.keys(persisted.metaQuota ?? {})).toEqual(['metaSuccess', 'metaError']);

    const persistedString = JSON.stringify(persisted);
    expect(persistedString).not.toContain('dca:');
    expect(persistedString).not.toContain('LLM|');
  });

  it('drops legacy and unverified quota cache entries while canonicalizing verified keys', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setClaudeQuota({
      legacy: { status: 'success', windows: [] },
      unverified: {
        status: 'success',
        windows: [],
        authFileKey: 'unverified',
        authFileIdentityVerified: false,
      },
      oldFilenameKey: {
        status: 'success',
        windows: [],
        authFileKey: 'canonical-credential-key',
        authFileIdentityVerified: true,
      },
    });

    const persisted = await readPersistedQuotaState();
    expect(Object.keys(persisted.claudeQuota ?? {})).toEqual(['canonical-credential-key']);
  });

  it('hydrates persisted quota success and error states', async () => {
    const { flushPendingQuotaCacheWrites, useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setCodexQuota({
      failed: {
        status: 'error',
        windows: [],
        error: 'failed',
        errorStatus: 401,
        authFileKey: 'failed',
        authFileIdentityVerified: true,
      },
    });
    useQuotaStore.getState().setClaudeQuota({
      claudeSuccess: {
        status: 'success',
        windows: [],
        authFileKey: 'claudeSuccess',
        authFileIdentityVerified: true,
      },
    });
    useQuotaStore.getState().setDevinQuota({
      devinSuccess: {
        status: 'success',
        windows: [],
        observedAtMs: 1_000,
        plan: 'Team',
        planStartMs: null,
        planEndMs: null,
        authFileKey: 'devinSuccess',
        authFileIdentityVerified: true,
      },
      devinError: {
        status: 'error',
        windows: [],
        observedAtMs: null,
        plan: null,
        planStartMs: null,
        planEndMs: null,
        error: 'devin failed',
        errorStatus: 502,
        authFileKey: 'devinError',
        authFileIdentityVerified: true,
      },
    });
    useQuotaStore.getState().setMetaQuota({
      metaSuccess: {
        status: 'success',
        windows: [],
        observedAtMs: 1_000,
        plan: 'Meta Pro',
        isSubscriptionActive: true,
        quotaInventoryObserved: true,
        authFileKey: 'metaSuccess',
        authFileIdentityVerified: true,
      },
      metaError: {
        status: 'error',
        windows: [],
        observedAtMs: 1_000,
        plan: null,
        isSubscriptionActive: null,
        quotaInventoryObserved: false,
        error: 'meta failed',
        errorStatus: 502,
        authFileKey: 'metaError',
        authFileIdentityVerified: true,
      },
    });

    flushPendingQuotaCacheWrites();
    vi.resetModules();
    const { useQuotaStore: hydratedQuotaStore } = await import('./useQuotaStore');

    expect(hydratedQuotaStore.getState().codexQuota.failed).toMatchObject({
      status: 'error',
      errorStatus: 401,
    });
    expect(hydratedQuotaStore.getState().claudeQuota.claudeSuccess).toMatchObject({
      status: 'success',
    });
    expect(hydratedQuotaStore.getState().devinQuota.devinSuccess).toMatchObject({
      status: 'success',
      plan: 'Team',
    });
    expect(hydratedQuotaStore.getState().devinQuota.devinError).toMatchObject({
      status: 'error',
      error: 'devin failed',
      errorStatus: 502,
    });
    expect(hydratedQuotaStore.getState().metaQuota.metaSuccess).toMatchObject({
      status: 'success',
      plan: 'Meta Pro',
    });
    expect(hydratedQuotaStore.getState().metaQuota.metaError).toMatchObject({
      status: 'error',
      error: 'meta failed',
      errorStatus: 502,
    });
  });

  it('migrates an oversized version-zero Codex cache into the bounded durable cache', async () => {
    const { STORAGE_KEY_QUOTA_CACHE } = await import('@/utils/constants');
    const { obfuscatedStorage } = await import('@/services/storage/secureStorage');

    obfuscatedStorage.setItem(STORAGE_KEY_QUOTA_CACHE, {
      state: {
        cacheScope: 'legacy-scope',
        codexQuota: makeCodexQuotaRecords(405, { includeResetCredits: true }),
      },
      version: 0,
    });

    vi.resetModules();
    const { flushPendingQuotaCacheWrites, useQuotaStore: hydratedQuotaStore } = await import(
      './useQuotaStore'
    );

    expect(Object.keys(hydratedQuotaStore.getState().codexQuota)).toHaveLength(400);
    expect(hydratedQuotaStore.getState().codexQuota['codex-0000']).toBeUndefined();
    expect(hydratedQuotaStore.getState().codexQuota['codex-0404']).toBeDefined();

    flushPendingQuotaCacheWrites();
    const persisted = await readPersistedQuotaState();
    expect(Object.keys(persisted.codexQuota ?? {})).toHaveLength(400);
    expect(persisted.codexQuota?.['codex-0000']).toBeUndefined();
    expect(persisted.codexQuota?.['codex-0404']?.rateLimitResetCredits).toBeUndefined();
  });

  it('normalizes legacy scalar Codex spend-control limits while hydrating quota cache', async () => {
    const { STORAGE_KEY_QUOTA_CACHE } = await import('@/utils/constants');
    const { obfuscatedStorage } = await import('@/services/storage/secureStorage');

    obfuscatedStorage.setItem(STORAGE_KEY_QUOTA_CACHE, {
      state: {
        cacheScope: 'legacy-scope',
        codexQuota: {
          legacyCodex: {
            status: 'success',
            windows: [],
            authFileKey: 'legacy-codex',
            authFileIdentityVerified: true,
            fetchedAtMs: 2_000,
            spendControlReached: true,
            spendControlIndividualLimit: 200,
          },
          canonicalCodex: {
            status: 'success',
            windows: [],
            authFileKey: 'canonical-codex',
            authFileIdentityVerified: true,
            fetchedAtMs: 3_000,
            spendControlReached: true,
            spendControlIndividualLimit: {
              source: 'workspace_spend_controls',
              unit: 'credit',
              limit: '100',
              used: '100',
              remaining: '0',
              usedPercent: 100,
              remainingPercent: 0,
              resetAfterSeconds: 3600,
              resetAtMs: 1_793_491_200_000,
            },
          },
        },
      },
      version: 0,
    });

    vi.resetModules();
    const { useQuotaStore: hydratedQuotaStore } = await import('./useQuotaStore');

    expect(hydratedQuotaStore.getState().codexQuota['legacy-codex']).toMatchObject({
      status: 'success',
      spendControlReached: true,
      spendControlIndividualLimit: {
        source: null,
        unit: null,
        limit: '200',
        used: null,
        remaining: null,
        usedPercent: null,
        remainingPercent: null,
        resetAfterSeconds: null,
        resetAtMs: null,
      },
    });
    expect(
      hydratedQuotaStore.getState().codexQuota['canonical-codex'].spendControlIndividualLimit
    ).toEqual({
      source: 'workspace_spend_controls',
      unit: 'credit',
      limit: '100',
      used: '100',
      remaining: '0',
      usedPercent: 100,
      remainingPercent: 0,
      resetAfterSeconds: 3600,
      resetAtMs: 1_793_491_200_000,
    });
  });

  it('clears quota state and persisted quota cache together', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setCodexQuota({
      manual: {
        status: 'success',
        windows: [],
        authFileKey: 'manual',
        authFileIdentityVerified: true,
        fetchedAtMs: 2_000,
      },
    });
    useQuotaStore.getState().setMetaQuota({
      manualMeta: {
        status: 'success',
        windows: [],
        observedAtMs: 2_000,
        plan: null,
        isSubscriptionActive: null,
        quotaInventoryObserved: false,
        authFileKey: 'manualMeta',
        authFileIdentityVerified: true,
      },
    });

    useQuotaStore.getState().clearQuotaCache();

    expect(useQuotaStore.getState().codexQuota).toEqual({});
    expect(useQuotaStore.getState().metaQuota).toEqual({});
    expect(await readPersistedQuotaState()).toMatchObject({
      antigravityQuota: {},
      claudeQuota: {},
      codexQuota: {},
      devinQuota: {},
      kimiQuota: {},
      metaQuota: {},
      xaiQuota: {},
    });
  });

  it('keeps quota for the same connection scope and clears it when the scope changes', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().activateQuotaCacheScope('scope-a');
    useQuotaStore.getState().setCodexQuota({
      manual: {
        status: 'success',
        windows: [],
        authFileKey: 'manual',
        authFileIdentityVerified: true,
        fetchedAtMs: 2_000,
      },
    });
    const generation = useQuotaStore.getState().cacheGeneration;

    useQuotaStore.getState().activateQuotaCacheScope('scope-a');
    expect(useQuotaStore.getState().cacheGeneration).toBe(generation);
    expect(Object.keys(useQuotaStore.getState().codexQuota)).toEqual(['manual']);

    useQuotaStore.getState().activateQuotaCacheScope('scope-b');
    expect(useQuotaStore.getState().cacheGeneration).toBe(generation + 1);
    expect(useQuotaStore.getState().codexQuota).toEqual({});
    expect(await readPersistedQuotaScope()).toBe('scope-b');
  });

  it('rejects stale async commits after the connection scope changes', async () => {
    const {
      captureQuotaCacheGeneration,
      commitIfQuotaCacheCurrent,
      isQuotaCacheGenerationCurrent,
      useQuotaStore,
    } = await import('./useQuotaStore');

    useQuotaStore.getState().activateQuotaCacheScope('scope-a');
    const staleGeneration = captureQuotaCacheGeneration();
    expect(isQuotaCacheGenerationCurrent(staleGeneration)).toBe(true);

    useQuotaStore.getState().activateQuotaCacheScope('scope-b');
    expect(isQuotaCacheGenerationCurrent(staleGeneration)).toBe(false);

    let committed = false;
    expect(
      commitIfQuotaCacheCurrent(staleGeneration, () => {
        committed = true;
      })
    ).toBe(false);
    expect(committed).toBe(false);
  });

  it('keeps only the 800 most recently refreshed Codex quota states in memory', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setCodexQuota(makeCodexQuotaRecords(805));

    const quota = useQuotaStore.getState().codexQuota;
    expect(Object.keys(quota)).toHaveLength(800);
    expect(quota['codex-0000']).toBeUndefined();
    expect(quota['codex-0804']).toBeDefined();
  });

  it('persists only the 400 newest compact Codex quota states', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setCodexQuota(
      makeCodexQuotaRecords(405, { includeResetCredits: true })
    );

    const persisted = await readPersistedQuotaState();
    expect(Object.keys(persisted.codexQuota ?? {})).toHaveLength(400);
    expect(persisted.codexQuota?.['codex-0404']?.rateLimitResetCredits).toBeUndefined();
  });

  it('persists only the 50 newest records for a non-Codex quota provider', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setClaudeQuota(makeClaudeQuotaRecords(55));

    const persisted = await readPersistedQuotaState();
    expect(Object.keys(persisted.claudeQuota ?? {})).toHaveLength(50);
    expect(persisted.claudeQuota?.['claude-0000']).toBeUndefined();
    expect(persisted.claudeQuota?.['claude-0054']).toBeDefined();
  });

  it('keeps the durable quota cache envelope below the serialized byte budget', async () => {
    const { STORAGE_KEY_QUOTA_CACHE } = await import('@/utils/constants');
    const { obfuscatedStorage } = await import('@/services/storage/secureStorage');
    const { flushPendingQuotaCacheWrites, useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setCodexQuota(makeCodexQuotaRecords(405, { windowLabelBytes: 10_000 }));

    flushPendingQuotaCacheWrites();
    const persisted = obfuscatedStorage.getItem(STORAGE_KEY_QUOTA_CACHE);
    const bytes = new TextEncoder().encode(JSON.stringify(persisted)).byteLength;
    expect(bytes).toBeLessThanOrEqual(QUOTA_CACHE_EXPECTED_MAX_SERIALIZED_BYTES);
  });

  it('coalesces a refresh burst into one durable quota-cache write', async () => {
    const trackedStorage = createTrackingStorage();
    vi.stubGlobal('localStorage', trackedStorage);
    const { flushPendingQuotaCacheWrites, useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setCodexQuota(makeCodexQuotaRecords(1));
    useQuotaStore.getState().setCodexQuota(makeCodexQuotaRecords(2));

    expect(trackedStorage.setItem).not.toHaveBeenCalled();
    flushPendingQuotaCacheWrites();
    expect(trackedStorage.setItem).toHaveBeenCalledTimes(1);
  });

  it('does not add a timer while quota-cache persistence waits for browser idle time', async () => {
    vi.useFakeTimers();
    const { flushPendingQuotaCacheWrites, useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setCodexQuota(makeCodexQuotaRecords(1));

    expect(vi.getTimerCount()).toBe(0);
    flushPendingQuotaCacheWrites();
  });

  it('keeps live quota state when browser durable storage reports quota exhaustion', async () => {
    vi.stubGlobal('localStorage', createQuotaExhaustedStorage());
    const { flushPendingQuotaCacheWrites, useQuotaStore } = await import('./useQuotaStore');

    expect(() =>
      useQuotaStore.getState().setCodexQuota({
        current: {
          status: 'success',
          windows: [],
          authFileKey: 'current',
          authFileIdentityVerified: true,
          fetchedAtMs: 1,
        },
      })
    ).not.toThrow();
    expect(() => flushPendingQuotaCacheWrites()).not.toThrow();
    expect(useQuotaStore.getState().codexQuota.current).toBeDefined();
  });
});
