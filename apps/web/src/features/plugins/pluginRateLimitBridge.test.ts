import { describe, expect, it, vi } from 'vitest';
import {
  RATE_LIMIT_BRIDGE_MAX_PENDING_SAVES,
  RATE_LIMIT_BRIDGE_PROTOCOL,
  appendRateLimitBridgeParentOrigin,
  buildRateLimitConfigPatch,
  createRateLimitBridgeHost,
  normalizeRateLimitBridgeSettings,
  parseRateLimitBridgeRequest,
  parseTrustedRateLimitBridgeRequest,
  resolveRateLimitBridgeFrameOrigin,
} from './pluginRateLimitBridge';

const createDeferred = <T,>() => {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
};

const legacySaveSettings = {
  enabled: true,
  rpm: 1,
  minInterval: '1s',
  maxQueueWait: '35s',
  maxPendingRequests: 500,
};

const modernSaveSettings = {
  ...legacySaveSettings,
  concurrencyEnabled: true,
  maxConcurrentPerAuth: 1,
  affinityWait: '15s',
  allCandidatesWait: '30s',
  retryInitialInterval: '250ms',
  retryMaxInterval: '1s',
  rebindSessionAfterAffinityWait: true,
};

const createSaveEvent = (
  requestID: string,
  settings: Record<string, unknown>,
  source: MessageEventSource
) => ({
  data: {
    protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
    pluginID: 'per-auth-rate-limit',
    requestID,
    operation: 'save',
    settings,
  },
  origin: 'https://cpa.kyon666.top',
  source,
});

describe('per-auth rate limit iframe bridge', () => {
  it('accepts scoped load and save commands and produces a minimal config patch', () => {
    const load = parseRateLimitBridgeRequest({
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID: 'load_1',
      operation: 'load',
    });
    expect(load).toEqual({
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID: 'load_1',
      operation: 'load',
    });

    const save = parseRateLimitBridgeRequest({
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID: 'save_1',
      operation: 'save',
      settings: {
        enabled: false,
        rpm: 30,
        minInterval: '10s',
        maxQueueWait: '35s',
        maxPendingRequests: 500,
        concurrencyEnabled: true,
        maxConcurrentPerAuth: 8,
        affinityWait: '15s',
        allCandidatesWait: '30s',
        retryInitialInterval: '250ms',
        retryMaxInterval: '1s',
        rebindSessionAfterAffinityWait: false,
      },
    });
    expect(save).toMatchObject({
      operation: 'save',
      settings: {
        rpm: 30,
        minInterval: '10s',
        maxQueueWait: '35s',
        maxPendingRequests: 500,
        concurrencyEnabled: true,
        maxConcurrentPerAuth: 8,
        affinityWait: '15s',
        allCandidatesWait: '30s',
        retryInitialInterval: '250ms',
        retryMaxInterval: '1s',
        rebindSessionAfterAffinityWait: false,
        egressConcurrencyEnabled: false,
        maxConcurrentPerEgress: 0,
      },
    });
    if (!save || save.operation !== 'save') throw new Error('expected save request');

    expect(buildRateLimitConfigPatch(save)).toEqual({
      enabled: false,
      default: { rpm: 30, min_interval: '10s', max_concurrent_per_auth: 8 },
      max_queue_wait: '35s',
      max_pending_requests: 500,
      concurrency: {
        enabled: true,
        affinity_wait: '15s',
        all_candidates_wait: '30s',
        retry_initial_interval: '250ms',
        retry_max_interval: '1s',
        rebind_session_after_affinity_wait: false,
      },
      accounts: null,
    });
  });

  it('round-trips concurrent scheduling settings between the iframe and plugin config', () => {
    expect(
      normalizeRateLimitBridgeSettings({
        enabled: true,
        default: { rpm: 0, min_interval: '0s', max_concurrent_per_auth: 40 },
        max_queue_wait: '35s',
        max_pending_requests: 500,
        concurrency: {
          enabled: true,
          affinity_wait: '20s',
          all_candidates_wait: '45s',
          retry_initial_interval: '300ms',
          retry_max_interval: '2s',
          rebind_session_after_affinity_wait: true,
        },
      })
    ).toEqual({
      enabled: true,
      rpm: 0,
      minInterval: '0s',
      maxQueueWait: '35s',
      maxPendingRequests: 500,
      concurrencyEnabled: true,
      maxConcurrentPerAuth: 40,
      affinityWait: '20s',
      allCandidatesWait: '45s',
      retryInitialInterval: '300ms',
      retryMaxInterval: '2s',
      rebindSessionAfterAffinityWait: true,
      egressConcurrencyEnabled: false,
      maxConcurrentPerEgress: 0,
      egressAccountPriorityAffinityEnabled: false,
      egressAuthRecoveryTTL: '900s',
      egress502HandoffTTL: '30s',
      egress429HandoffTTL: '120s',
    });
  });

  it('maps complete egress concurrency settings into the YAML concurrency patch', () => {
    const save = parseRateLimitBridgeRequest({
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID: 'egress_save_1',
      operation: 'save',
      settings: {
        enabled: true,
        rpm: 30,
        minInterval: '10s',
        maxQueueWait: '35s',
        maxPendingRequests: 500,
        concurrencyEnabled: true,
        maxConcurrentPerAuth: 8,
        affinityWait: '15s',
        allCandidatesWait: '30s',
        retryInitialInterval: '250ms',
        retryMaxInterval: '1s',
        rebindSessionAfterAffinityWait: false,
        egressConcurrencyEnabled: true,
        maxConcurrentPerEgress: 12,
        egressAccountPriorityAffinityEnabled: true,
        unknownSetting: 'must not cross the bridge',
      },
    });

    expect(save).toMatchObject({
      operation: 'save',
      settings: {
        egressConcurrencyEnabled: true,
        maxConcurrentPerEgress: 12,
        egressAccountPriorityAffinityEnabled: true,
      },
    });
    if (!save || save.operation !== 'save') throw new Error('expected egress save request');

    expect(save.settings).not.toHaveProperty('unknownSetting');
    expect(buildRateLimitConfigPatch(save)).toEqual({
      enabled: true,
      default: { rpm: 30, min_interval: '10s', max_concurrent_per_auth: 8 },
      max_queue_wait: '35s',
      max_pending_requests: 500,
      concurrency: {
        enabled: true,
        affinity_wait: '15s',
        all_candidates_wait: '30s',
        retry_initial_interval: '250ms',
        retry_max_interval: '1s',
        rebind_session_after_affinity_wait: false,
        egress_enabled: true,
        max_concurrent_per_egress: 12,
        egress_account_priority_affinity_enabled: true,
      },
      accounts: null,
    });
  });

  it('round-trips the three egress recovery TTLs between the modern iframe and YAML', () => {
    const save = parseRateLimitBridgeRequest({
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID: 'egress_recovery_ttl_save',
      operation: 'save',
      settings: {
        enabled: true,
        rpm: 30,
        minInterval: '10s',
        maxQueueWait: '35s',
        maxPendingRequests: 500,
        concurrencyEnabled: true,
        maxConcurrentPerAuth: 8,
        affinityWait: '15s',
        allCandidatesWait: '30s',
        retryInitialInterval: '250ms',
        retryMaxInterval: '1s',
        rebindSessionAfterAffinityWait: false,
        egressConcurrencyEnabled: true,
        maxConcurrentPerEgress: 12,
        egressAccountPriorityAffinityEnabled: true,
        egressAuthRecoveryTTL: '900s',
        egress502HandoffTTL: '30s',
        egress429HandoffTTL: '120s',
      },
    });

    expect(save).toMatchObject({
      operation: 'save',
      hasEgressRecoverySettings: true,
      settings: {
        egressAuthRecoveryTTL: '900s',
        egress502HandoffTTL: '30s',
        egress429HandoffTTL: '120s',
      },
    });
    if (!save || save.operation !== 'save') throw new Error('expected egress recovery TTL save');

    expect(buildRateLimitConfigPatch(save)).toMatchObject({
      concurrency: expect.objectContaining({
        egress_auth_recovery_ttl: '900s',
        egress_502_handoff_ttl: '30s',
        egress_429_handoff_ttl: '120s',
      }),
    });
    expect(
      normalizeRateLimitBridgeSettings({
        enabled: true,
        default: { rpm: 30, min_interval: '10s', max_concurrent_per_auth: 8 },
        max_queue_wait: '35s',
        max_pending_requests: 500,
        concurrency: {
          enabled: true,
          egress_enabled: true,
          max_concurrent_per_egress: 12,
          egress_account_priority_affinity_enabled: true,
          egress_auth_recovery_ttl: '900s',
          egress_502_handoff_ttl: '30s',
          egress_429_handoff_ttl: '120s',
        },
      })
    ).toMatchObject({
      egressAuthRecoveryTTL: '900s',
      egress502HandoffTTL: '30s',
      egress429HandoffTTL: '120s',
    });
  });

  it('rejects a partial egress recovery TTL group', () => {
    expect(
      parseRateLimitBridgeRequest({
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'egress_recovery_ttl_partial',
        operation: 'save',
        settings: {
          enabled: true,
          rpm: 30,
          minInterval: '10s',
          maxQueueWait: '35s',
          maxPendingRequests: 500,
          concurrencyEnabled: true,
          maxConcurrentPerAuth: 8,
          affinityWait: '15s',
          allCandidatesWait: '30s',
          retryInitialInterval: '250ms',
          retryMaxInterval: '1s',
          rebindSessionAfterAffinityWait: false,
          egressConcurrencyEnabled: true,
          maxConcurrentPerEgress: 12,
          egressAccountPriorityAffinityEnabled: true,
          egressAuthRecoveryTTL: '15m',
        },
      })
    ).toBeNull();
  });

  it('rejects non-canonical, sub-second, and overflow egress recovery TTLs', () => {
    const completeSettings = {
      enabled: true,
      rpm: 30,
      minInterval: '10s',
      maxQueueWait: '35s',
      maxPendingRequests: 500,
      concurrencyEnabled: true,
      maxConcurrentPerAuth: 8,
      affinityWait: '15s',
      allCandidatesWait: '30s',
      retryInitialInterval: '250ms',
      retryMaxInterval: '1s',
      rebindSessionAfterAffinityWait: false,
      egressConcurrencyEnabled: true,
      maxConcurrentPerEgress: 12,
      egressAccountPriorityAffinityEnabled: true,
      egressAuthRecoveryTTL: '900s',
      egress502HandoffTTL: '30s',
      egress429HandoffTTL: '120s',
    };
    const invalidTTLs = [
      { egressAuthRecoveryTTL: '999ms' },
      { egress502HandoffTTL: '0.5s' },
      { egress429HandoffTTL: '9223372037s' },
      { egressAuthRecoveryTTL: '15m' },
    ];

    invalidTTLs.forEach((overrides, index) => {
      expect(
        parseRateLimitBridgeRequest({
          protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
          pluginID: 'per-auth-rate-limit',
          requestID: `egress_recovery_ttl_invalid_${index}`,
          operation: 'save',
          settings: { ...completeSettings, ...overrides },
        })
      ).toBeNull();
    });
  });

  it('accepts an older egress-capable iframe without clearing priority affinity', () => {
    const save = parseRateLimitBridgeRequest({
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID: 'egress_legacy_save_1',
      operation: 'save',
      settings: {
        enabled: true,
        rpm: 30,
        minInterval: '10s',
        maxQueueWait: '35s',
        maxPendingRequests: 500,
        concurrencyEnabled: true,
        maxConcurrentPerAuth: 8,
        affinityWait: '15s',
        allCandidatesWait: '30s',
        retryInitialInterval: '250ms',
        retryMaxInterval: '1s',
        rebindSessionAfterAffinityWait: false,
        egressConcurrencyEnabled: true,
        maxConcurrentPerEgress: 12,
      },
    });

    expect(save).toMatchObject({
      operation: 'save',
      hasEgressConcurrencySettings: true,
      hasEgressAccountPriorityAffinitySetting: false,
      settings: { egressAccountPriorityAffinityEnabled: false },
    });
    if (!save || save.operation !== 'save') throw new Error('expected legacy egress save request');

    expect(buildRateLimitConfigPatch(save)).toEqual({
      enabled: true,
      default: { rpm: 30, min_interval: '10s', max_concurrent_per_auth: 8 },
      max_queue_wait: '35s',
      max_pending_requests: 500,
      concurrency: {
        enabled: true,
        affinity_wait: '15s',
        all_candidates_wait: '30s',
        retry_initial_interval: '250ms',
        retry_max_interval: '1s',
        rebind_session_after_affinity_wait: false,
        egress_enabled: true,
        max_concurrent_per_egress: 12,
      },
      accounts: null,
    });
  });

  it('rejects partial, malformed, or out-of-range egress concurrency settings', () => {
    const completeAccountConcurrency = {
      concurrencyEnabled: true,
      maxConcurrentPerAuth: 8,
      affinityWait: '15s',
      allCandidatesWait: '30s',
      retryInitialInterval: '250ms',
      retryMaxInterval: '1s',
      rebindSessionAfterAffinityWait: true,
    };
    const baseSettings = {
      enabled: true,
      rpm: 1,
      minInterval: '1s',
      maxQueueWait: '35s',
      maxPendingRequests: 1,
    };
    const invalidSettings = [
      { ...baseSettings, egressConcurrencyEnabled: true },
      { ...baseSettings, ...completeAccountConcurrency, egressConcurrencyEnabled: true },
      {
        ...baseSettings,
        ...completeAccountConcurrency,
        egressConcurrencyEnabled: true,
        maxConcurrentPerEgress: -1,
      },
      {
        ...baseSettings,
        ...completeAccountConcurrency,
        egressConcurrencyEnabled: true,
        maxConcurrentPerEgress: 2_147_483_648,
      },
      {
        ...baseSettings,
        ...completeAccountConcurrency,
        egressConcurrencyEnabled: 'true',
        maxConcurrentPerEgress: 4,
      },
      {
        ...baseSettings,
        ...completeAccountConcurrency,
        egressConcurrencyEnabled: true,
        maxConcurrentPerEgress: 1.5,
      },
      {
        ...baseSettings,
        ...completeAccountConcurrency,
        egressConcurrencyEnabled: true,
        maxConcurrentPerEgress: 4,
        maxConcurrentPerAuth: '8',
      },
      {
        ...baseSettings,
        ...completeAccountConcurrency,
        egressAccountPriorityAffinityEnabled: true,
      },
    ];

    invalidSettings.forEach((settings, index) => {
      expect(
        parseRateLimitBridgeRequest({
          protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
          pluginID: 'per-auth-rate-limit',
          requestID: `egress_invalid_${index}`,
          operation: 'save',
          settings,
        })
      ).toBeNull();
    });
  });

  it('rejects priority affinity when account or egress concurrency is inactive', () => {
    const completeSettings = {
      enabled: true,
      rpm: 1,
      minInterval: '1s',
      maxQueueWait: '35s',
      maxPendingRequests: 1,
      concurrencyEnabled: true,
      maxConcurrentPerAuth: 8,
      affinityWait: '15s',
      allCandidatesWait: '30s',
      retryInitialInterval: '250ms',
      retryMaxInterval: '1s',
      rebindSessionAfterAffinityWait: true,
      egressConcurrencyEnabled: true,
      maxConcurrentPerEgress: 4,
      egressAccountPriorityAffinityEnabled: true,
      egressAuthRecoveryTTL: '900s',
      egress502HandoffTTL: '30s',
      egress429HandoffTTL: '120s',
    };
    const invalidSettings = [
      {
        requestID: 'priority_account_disabled',
        settings: { ...completeSettings, concurrencyEnabled: false },
      },
      {
        requestID: 'priority_egress_disabled',
        settings: { ...completeSettings, egressConcurrencyEnabled: false },
      },
      {
        requestID: 'priority_egress_zero',
        settings: { ...completeSettings, maxConcurrentPerEgress: 0 },
      },
    ];

    invalidSettings.forEach(({ requestID, settings }) => {
      expect(
        parseRateLimitBridgeRequest({
          protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
          pluginID: 'per-auth-rate-limit',
          requestID,
          operation: 'save',
          settings,
        })
      ).toBeNull();
    });
  });

  it('keeps legacy v1 saves working without overwriting concurrency settings', () => {
    const save = parseRateLimitBridgeRequest({
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID: 'legacy_save_1',
      operation: 'save',
      settings: {
        enabled: true,
        rpm: 12,
        minInterval: '2s',
        maxQueueWait: '35s',
        maxPendingRequests: 500,
      },
    });

    expect(save).toMatchObject({ operation: 'save', legacy: true });
    if (!save || save.operation !== 'save') throw new Error('expected legacy save request');

    expect(buildRateLimitConfigPatch(save)).toEqual({
      enabled: true,
      default: { rpm: 12, min_interval: '2s' },
      max_queue_wait: '35s',
      max_pending_requests: 500,
      accounts: null,
    });
  });

  it('rejects unscoped or malformed requests before they reach the authenticated host API', () => {
    const invalid = [
      {},
      {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'another-plugin',
        requestID: 'x',
        operation: 'load',
      },
      {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'save_1',
        operation: 'save',
        settings: {
          enabled: true,
          rpm: -1,
          minInterval: '10s',
          maxQueueWait: '35s',
          maxPendingRequests: 500,
        },
      },
      {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'save_1',
        operation: 'save',
        settings: {
          enabled: true,
          rpm: 1,
          minInterval: '-1s',
          maxQueueWait: '35s',
          maxPendingRequests: 500,
        },
      },
      {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'save_1',
        operation: 'save',
        settings: {
          enabled: true,
          rpm: 1,
          minInterval: '1s',
          maxQueueWait: '35s',
          maxPendingRequests: -1,
        },
      },
      {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'save_1',
        operation: 'save',
        settings: {
          enabled: true,
          rpm: 1,
          minInterval: '1s',
          maxQueueWait: '35s',
          maxPendingRequests: 1,
          concurrencyEnabled: true,
          maxConcurrentPerAuth: -1,
          affinityWait: '15s',
          allCandidatesWait: '30s',
          retryInitialInterval: '250ms',
          retryMaxInterval: '1s',
          rebindSessionAfterAffinityWait: true,
        },
      },
      {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'save_1',
        operation: 'save',
        settings: {
          enabled: true,
          rpm: 1,
          minInterval: '1s',
          maxQueueWait: '35s',
          maxPendingRequests: 1,
          concurrencyEnabled: true,
          maxConcurrentPerAuth: 1,
          affinityWait: '-1s',
          allCandidatesWait: '30s',
          retryInitialInterval: '250ms',
          retryMaxInterval: '1s',
          rebindSessionAfterAffinityWait: true,
        },
      },
      {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'save_1',
        operation: 'save',
        settings: {
          enabled: true,
          rpm: 1,
          minInterval: '1s',
          maxQueueWait: '35s',
          maxPendingRequests: 1,
          concurrencyEnabled: true,
        },
      },
    ];

    invalid.forEach((message) => expect(parseRateLimitBridgeRequest(message)).toBeNull());
  });

  it('requires the active iframe window and exact resource origin before accepting a command', () => {
    const message = {
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID: 'load_2',
      operation: 'load',
    };

    expect(
      parseTrustedRateLimitBridgeRequest(message, {
        sourceMatchesFrame: true,
        origin: 'https://cpa.kyon666.top',
        expectedOrigin: 'https://cpa.kyon666.top',
      })
    ).toMatchObject({ operation: 'load' });
    expect(
      parseTrustedRateLimitBridgeRequest(message, {
        sourceMatchesFrame: false,
        origin: 'https://cpa.kyon666.top',
        expectedOrigin: 'https://cpa.kyon666.top',
      })
    ).toBeNull();
    expect(
      parseTrustedRateLimitBridgeRequest(message, {
        sourceMatchesFrame: true,
        origin: 'https://evil.example',
        expectedOrigin: 'https://cpa.kyon666.top',
      })
    ).toBeNull();
  });

  it('uses the authenticated host callbacks only for the active iframe and emits a minimal save result', async () => {
    const trustedSource = {} as MessageEventSource;
    const sent: unknown[] = [];
    const patches: unknown[] = [];
    const handle = createRateLimitBridgeHost({
      expectedOrigin: 'https://cpa.kyon666.top',
      isActiveFrameSource: (source) => source === trustedSource,
      getConfig: async () => ({
        enabled: true,
        default: { rpm: 8, min_interval: '0s', max_concurrent_per_auth: 1 },
        max_queue_wait: '35s',
        max_pending_requests: 500,
        concurrency: {
          enabled: true,
          affinity_wait: '15s',
          all_candidates_wait: '30s',
          retry_initial_interval: '250ms',
          retry_max_interval: '1s',
          rebind_session_after_affinity_wait: true,
        },
      }),
      patchConfig: async (patch) => {
        patches.push(patch);
      },
      reply: (_source, message) => sent.push(message),
    });

    await handle({
      data: {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'save_2',
        operation: 'save',
        settings: {
          enabled: true,
          rpm: 6,
          minInterval: '10s',
          maxQueueWait: '1m',
          maxPendingRequests: 42,
          concurrencyEnabled: true,
          maxConcurrentPerAuth: 1,
          affinityWait: '15s',
          allCandidatesWait: '30s',
          retryInitialInterval: '250ms',
          retryMaxInterval: '1s',
          rebindSessionAfterAffinityWait: true,
        },
      },
      origin: 'https://cpa.kyon666.top',
      source: trustedSource,
    });
    await handle({
      data: {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'evil_1',
        operation: 'load',
      },
      origin: 'https://evil.example',
      source: trustedSource,
    });

    expect(patches).toEqual([
      {
        enabled: true,
        default: { rpm: 6, min_interval: '10s', max_concurrent_per_auth: 1 },
        max_queue_wait: '1m',
        max_pending_requests: 42,
        concurrency: {
          enabled: true,
          affinity_wait: '15s',
          all_candidates_wait: '30s',
          retry_initial_interval: '250ms',
          retry_max_interval: '1s',
          rebind_session_after_affinity_wait: true,
        },
        accounts: null,
      },
    ]);
    expect(sent).toEqual([
      expect.objectContaining({ requestID: 'save_2', operation: 'result', ok: true }),
    ]);
  });

  it('disposes a blocked save queue without starting queued APIs or replying', async () => {
    const trustedSource = {} as MessageEventSource;
    const firstPatchStarted = createDeferred<void>();
    const releaseFirstPatch = createDeferred<void>();
    const getConfig = vi.fn(async () => ({}));
    const patchConfig = vi.fn(async () => {
      firstPatchStarted.resolve();
      await releaseFirstPatch.promise;
    });
    const responses: unknown[] = [];
    const handle = createRateLimitBridgeHost({
      expectedOrigin: 'https://cpa.kyon666.top',
      isActiveFrameSource: (source) => source === trustedSource,
      getConfig,
      patchConfig,
      reply: (_source, response) => responses.push(response),
    });

    const firstSave = handle(createSaveEvent('dispose_first', legacySaveSettings, trustedSource));
    const secondSave = handle(createSaveEvent('dispose_second', modernSaveSettings, trustedSource));
    await firstPatchStarted.promise;

    handle.dispose();
    handle.dispose();
    releaseFirstPatch.resolve();
    await Promise.all([firstSave, secondSave]);

    expect(getConfig).not.toHaveBeenCalled();
    expect(patchConfig).toHaveBeenCalledTimes(1);
    expect(responses).toEqual([]);
  });

  it('does not reply when a load finishes after the host is disposed', async () => {
    const trustedSource = {} as MessageEventSource;
    const loadStarted = createDeferred<void>();
    const releaseLoad = createDeferred<void>();
    const responses: unknown[] = [];
    const handle = createRateLimitBridgeHost({
      expectedOrigin: 'https://cpa.kyon666.top',
      isActiveFrameSource: (source) => source === trustedSource,
      getConfig: async () => {
        loadStarted.resolve();
        await releaseLoad.promise;
        return {};
      },
      patchConfig: async () => undefined,
      reply: (_source, response) => responses.push(response),
    });

    const load = handle({
      data: {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'disposed_load',
        operation: 'load',
      },
      origin: 'https://cpa.kyon666.top',
      source: trustedSource,
    });
    await loadStarted.promise;

    handle.dispose();
    releaseLoad.resolve();
    await load;

    expect(responses).toEqual([]);
  });

  it('rejects the seventeenth pending save without calling its APIs', async () => {
    const expectedPendingSaveLimit = 16;
    const trustedSource = {} as MessageEventSource;
    const firstPatchStarted = createDeferred<void>();
    const releaseFirstPatch = createDeferred<void>();
    const patches: unknown[] = [];
    const responses: unknown[] = [];
    const getConfig = vi.fn(async () => ({}));
    const patchConfig = vi.fn(async (patch: unknown) => {
      patches.push(patch);
      if (patches.length === 1) {
        firstPatchStarted.resolve();
        await releaseFirstPatch.promise;
      }
    });
    const handle = createRateLimitBridgeHost({
      expectedOrigin: 'https://cpa.kyon666.top',
      isActiveFrameSource: (source) => source === trustedSource,
      getConfig,
      patchConfig,
      reply: (_source, response) => responses.push(response),
    });

    const acceptedSaves = Array.from({ length: expectedPendingSaveLimit }, (_, index) =>
      handle(createSaveEvent(`pending_${index + 1}`, legacySaveSettings, trustedSource))
    );
    await firstPatchStarted.promise;

    const rejectedSave = handle(createSaveEvent('pending_17', legacySaveSettings, trustedSource));
    await Promise.resolve();
    const patchesBeforeRelease = patches.length;
    const getConfigCallsBeforeRelease = getConfig.mock.calls.length;
    const responsesBeforeRelease = [...responses];

    releaseFirstPatch.resolve();
    await Promise.all([...acceptedSaves, rejectedSave]);

    expect(RATE_LIMIT_BRIDGE_MAX_PENDING_SAVES).toBe(expectedPendingSaveLimit);
    expect(patchesBeforeRelease).toBe(1);
    expect(getConfigCallsBeforeRelease).toBe(0);
    expect(responsesBeforeRelease).toEqual([
      expect.objectContaining({
        requestID: 'pending_17',
        operation: 'result',
        ok: false,
      }),
    ]);
    expect(patchConfig).toHaveBeenCalledTimes(expectedPendingSaveLimit);
  });

  it('round-trips modern egress priority affinity through the host load/save path', async () => {
    const trustedSource = {} as MessageEventSource;
    const modernSettings = {
      enabled: true,
      rpm: 8,
      minInterval: '0s',
      maxQueueWait: '35s',
      maxPendingRequests: 500,
      concurrencyEnabled: true,
      maxConcurrentPerAuth: 4,
      affinityWait: '15s',
      allCandidatesWait: '30s',
      retryInitialInterval: '250ms',
      retryMaxInterval: '1s',
      rebindSessionAfterAffinityWait: true,
      egressConcurrencyEnabled: true,
      maxConcurrentPerEgress: 4,
      egressAccountPriorityAffinityEnabled: true,
      egressAuthRecoveryTTL: '900s',
      egress502HandoffTTL: '30s',
      egress429HandoffTTL: '120s',
    };
    let savedConfig: Record<string, unknown> = {
      enabled: true,
      default: { rpm: 8, min_interval: '0s', max_concurrent_per_auth: 4 },
      max_queue_wait: '35s',
      max_pending_requests: 500,
      concurrency: {
        enabled: true,
        affinity_wait: '15s',
        all_candidates_wait: '30s',
        retry_initial_interval: '250ms',
        retry_max_interval: '1s',
        rebind_session_after_affinity_wait: true,
        egress_enabled: true,
        max_concurrent_per_egress: 4,
        egress_account_priority_affinity_enabled: false,
      },
    };
    let loadedSettings: typeof modernSettings | undefined;
    const responses: unknown[] = [];
    const handle = createRateLimitBridgeHost({
      expectedOrigin: 'https://cpa.kyon666.top',
      isActiveFrameSource: (source) => source === trustedSource,
      getConfig: async () => savedConfig,
      patchConfig: async (patch) => {
        savedConfig = { ...savedConfig, ...patch };
      },
      reply: (_source, message) => {
        responses.push(message);
        if (message.operation === 'result' && message.ok && message.requestID === 'modern_load') {
          loadedSettings = message.settings;
        }
      },
    });

    await handle({
      data: {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'modern_load',
        operation: 'load',
      },
      origin: 'https://cpa.kyon666.top',
      source: trustedSource,
    });

    expect(loadedSettings).toEqual({
      ...modernSettings,
      egressAccountPriorityAffinityEnabled: false,
    });
    if (!loadedSettings) throw new Error('expected modern load settings');

    await handle({
      data: {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'modern_save',
        operation: 'save',
        settings: { ...loadedSettings, egressAccountPriorityAffinityEnabled: true },
      },
      origin: 'https://cpa.kyon666.top',
      source: trustedSource,
    });

    expect(savedConfig).toMatchObject({
      concurrency: expect.objectContaining({
        egress_account_priority_affinity_enabled: true,
      }),
    });
    expect(responses).toHaveLength(2);
    expect(responses[1]).toMatchObject({ requestID: 'modern_save', operation: 'result', ok: true });
  });

  it('loads YAML egress priority affinity true into the iframe settings', async () => {
    const trustedSource = {} as MessageEventSource;
    const responses: unknown[] = [];
    const handle = createRateLimitBridgeHost({
      expectedOrigin: 'https://cpa.kyon666.top',
      isActiveFrameSource: (source) => source === trustedSource,
      getConfig: async () => ({
        enabled: true,
        default: { rpm: 8, min_interval: '0s', max_concurrent_per_auth: 4 },
        max_queue_wait: '35s',
        max_pending_requests: 500,
        concurrency: {
          enabled: true,
          egress_enabled: true,
          max_concurrent_per_egress: 4,
          egress_account_priority_affinity_enabled: true,
        },
      }),
      patchConfig: async () => undefined,
      reply: (_source, response) => responses.push(response),
    });

    await handle({
      data: {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'yaml_egress_priority_affinity_load',
        operation: 'load',
      },
      origin: 'https://cpa.kyon666.top',
      source: trustedSource,
    });

    expect(responses).toEqual([
      expect.objectContaining({
        requestID: 'yaml_egress_priority_affinity_load',
        operation: 'result',
        ok: true,
        settings: expect.objectContaining({
          egressAccountPriorityAffinityEnabled: true,
        }),
      }),
    ]);
  });

  it('preserves egress concurrency when an older v2 iframe saves account-only settings', async () => {
    const trustedSource = {} as MessageEventSource;
    const patches: unknown[] = [];
    const handle = createRateLimitBridgeHost({
      expectedOrigin: 'https://cpa.kyon666.top',
      isActiveFrameSource: (source) => source === trustedSource,
      getConfig: async () => ({
        enabled: true,
        default: { rpm: 8, min_interval: '0s', max_concurrent_per_auth: 1 },
        max_queue_wait: '35s',
        max_pending_requests: 500,
        concurrency: {
          enabled: true,
          affinity_wait: '15s',
          all_candidates_wait: '30s',
          retry_initial_interval: '250ms',
          retry_max_interval: '1s',
          rebind_session_after_affinity_wait: true,
          egress_enabled: true,
          max_concurrent_per_egress: 4,
        },
      }),
      patchConfig: async (patch) => {
        patches.push(patch);
      },
      reply: () => undefined,
    });

    await handle({
      data: {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'account_only_v2_save',
        operation: 'save',
        settings: {
          enabled: true,
          rpm: 6,
          minInterval: '10s',
          maxQueueWait: '1m',
          maxPendingRequests: 42,
          concurrencyEnabled: true,
          maxConcurrentPerAuth: 2,
          affinityWait: '15s',
          allCandidatesWait: '30s',
          retryInitialInterval: '250ms',
          retryMaxInterval: '1s',
          rebindSessionAfterAffinityWait: true,
        },
      },
      origin: 'https://cpa.kyon666.top',
      source: trustedSource,
    });

    expect(patches).toEqual([
      expect.objectContaining({
        concurrency: expect.objectContaining({
          egress_enabled: true,
          max_concurrent_per_egress: 4,
        }),
      }),
    ]);
  });

  it('preserves priority affinity when an older egress-capable iframe saves the old egress pair', async () => {
    const trustedSource = {} as MessageEventSource;
    let savedConfig: Record<string, unknown> = {
      enabled: true,
      default: { rpm: 8, min_interval: '0s', max_concurrent_per_auth: 4 },
      max_queue_wait: '35s',
      max_pending_requests: 500,
      concurrency: {
        enabled: true,
        affinity_wait: '15s',
        all_candidates_wait: '30s',
        retry_initial_interval: '250ms',
        retry_max_interval: '1s',
        rebind_session_after_affinity_wait: true,
        egress_enabled: true,
        max_concurrent_per_egress: 12,
        egress_account_priority_affinity_enabled: true,
        egress_auth_recovery_ttl: '15m',
        egress_502_handoff_ttl: '30s',
        egress_429_handoff_ttl: '2m',
      },
    };
    const handle = createRateLimitBridgeHost({
      expectedOrigin: 'https://cpa.kyon666.top',
      isActiveFrameSource: (source) => source === trustedSource,
      getConfig: async () => savedConfig,
      patchConfig: async (patch) => {
        savedConfig = { ...savedConfig, ...patch };
      },
      reply: () => undefined,
    });

    await handle({
      data: {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'egress_legacy_pair_save',
        operation: 'save',
        settings: {
          enabled: true,
          rpm: 6,
          minInterval: '10s',
          maxQueueWait: '1m',
          maxPendingRequests: 42,
          concurrencyEnabled: true,
          maxConcurrentPerAuth: 2,
          affinityWait: '15s',
          allCandidatesWait: '30s',
          retryInitialInterval: '250ms',
          retryMaxInterval: '1s',
          rebindSessionAfterAffinityWait: true,
          egressConcurrencyEnabled: true,
          maxConcurrentPerEgress: 12,
        },
      },
      origin: 'https://cpa.kyon666.top',
      source: trustedSource,
    });

    expect(savedConfig).toMatchObject({
      concurrency: expect.objectContaining({
        egress_enabled: true,
        max_concurrent_per_egress: 12,
        egress_account_priority_affinity_enabled: true,
        egress_auth_recovery_ttl: '15m',
        egress_502_handoff_ttl: '30s',
        egress_429_handoff_ttl: '2m',
      }),
    });
  });

  it('serializes overlapping egress saves so an omitted affinity field cannot erase a modern save', async () => {
    const trustedSource = {} as MessageEventSource;
    const initialConfig: Record<string, unknown> = {
      enabled: true,
      default: { rpm: 8, min_interval: '0s', max_concurrent_per_auth: 4 },
      max_queue_wait: '35s',
      max_pending_requests: 500,
      concurrency: {
        enabled: true,
        affinity_wait: '15s',
        all_candidates_wait: '30s',
        retry_initial_interval: '250ms',
        retry_max_interval: '1s',
        rebind_session_after_affinity_wait: true,
        egress_enabled: true,
        max_concurrent_per_egress: 4,
        egress_account_priority_affinity_enabled: false,
      },
    };
    let savedConfig = initialConfig;
    const firstPatchStarted = createDeferred<void>();
    const releaseFirstPatch = createDeferred<void>();
    const firstPatchCompleted = createDeferred<void>();
    const patches: Record<string, unknown>[] = [];
    const responses: unknown[] = [];
    const handle = createRateLimitBridgeHost({
      expectedOrigin: 'https://cpa.kyon666.top',
      isActiveFrameSource: (source) => source === trustedSource,
      getConfig: async () => ({ ...savedConfig }),
      patchConfig: async (patch) => {
        patches.push(patch);
        if (patches.length === 1) {
          firstPatchStarted.resolve();
          await releaseFirstPatch.promise;
          savedConfig = { ...savedConfig, ...patch };
          firstPatchCompleted.resolve();
          return;
        }
        await firstPatchCompleted.promise;
        savedConfig = { ...savedConfig, ...patch };
      },
      reply: (_source, message) => responses.push(message),
    });

    const modernSave = handle({
      data: {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'modern_affinity_save',
        operation: 'save',
        settings: {
          enabled: true,
          rpm: 8,
          minInterval: '0s',
          maxQueueWait: '35s',
          maxPendingRequests: 500,
          concurrencyEnabled: true,
          maxConcurrentPerAuth: 4,
          affinityWait: '15s',
          allCandidatesWait: '30s',
          retryInitialInterval: '250ms',
          retryMaxInterval: '1s',
          rebindSessionAfterAffinityWait: true,
          egressConcurrencyEnabled: true,
          maxConcurrentPerEgress: 8,
          egressAccountPriorityAffinityEnabled: true,
        },
      },
      origin: 'https://cpa.kyon666.top',
      source: trustedSource,
    });
    const oldEgressSave = handle({
      data: {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'old_egress_save',
        operation: 'save',
        settings: {
          enabled: true,
          rpm: 8,
          minInterval: '0s',
          maxQueueWait: '35s',
          maxPendingRequests: 500,
          concurrencyEnabled: true,
          maxConcurrentPerAuth: 4,
          affinityWait: '15s',
          allCandidatesWait: '30s',
          retryInitialInterval: '250ms',
          retryMaxInterval: '1s',
          rebindSessionAfterAffinityWait: true,
          egressConcurrencyEnabled: true,
          maxConcurrentPerEgress: 12,
        },
      },
      origin: 'https://cpa.kyon666.top',
      source: trustedSource,
    });

    await firstPatchStarted.promise;
    releaseFirstPatch.resolve();
    await Promise.all([modernSave, oldEgressSave]);

    expect(savedConfig).toMatchObject({
      concurrency: expect.objectContaining({
        egress_enabled: true,
        max_concurrent_per_egress: 12,
        egress_account_priority_affinity_enabled: true,
      }),
    });
    expect(patches).toHaveLength(2);
    expect(patches[1].concurrency).toMatchObject({
      egress_account_priority_affinity_enabled: true,
    });
    expect(responses).toEqual([
      expect.objectContaining({ requestID: 'modern_affinity_save', operation: 'result', ok: true }),
      expect.objectContaining({ requestID: 'old_egress_save', operation: 'result', ok: true }),
    ]);
  });

  it('keeps the resource origin exact while carrying only the non-secret parent origin', () => {
    const source = appendRateLimitBridgeParentOrigin(
      'https://cpa.kyon666.top/v0/resource/plugins/per-auth-rate-limit/settings?theme=dark',
      'https://cpam.kyon666.top'
    );
    const url = new URL(source);

    expect(url.origin).toBe('https://cpa.kyon666.top');
    expect(url.searchParams.get('theme')).toBe('dark');
    expect(url.searchParams.get('cpamp_bridge_parent_origin')).toBe('https://cpam.kyon666.top');
    expect(resolveRateLimitBridgeFrameOrigin(source, 'https://cpam.kyon666.top')).toBe(
      'https://cpa.kyon666.top'
    );
  });

  it('accepts a bounded diagnostics request and defaults its event limit', () => {
    const explicit = parseRateLimitBridgeRequest({
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID: 'egress_diag_100',
      operation: 'diagnostics',
      limit: 100,
    });
    const implicit = parseRateLimitBridgeRequest({
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID: 'egress_diag_default',
      operation: 'diagnostics',
    });

    expect(explicit).toEqual({
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID: 'egress_diag_100',
      operation: 'diagnostics',
      limit: 100,
    });
    expect(implicit).toEqual({
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID: 'egress_diag_default',
      operation: 'diagnostics',
      limit: 100,
    });
  });

  it('rejects invalid diagnostics limits and strips arbitrary routing fields', () => {
    const base = {
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID: 'egress_diag_invalid',
      operation: 'diagnostics',
    } as const;

    for (const limit of [0, -1, 1025, 1.5, '100', null]) {
      expect(parseRateLimitBridgeRequest({ ...base, limit })).toBeNull();
    }

    expect(
      parseRateLimitBridgeRequest({
        ...base,
        requestID: 'egress_diag_stripped',
        limit: 1,
        url: 'https://attacker.invalid/management',
        path: '/v0/management/config',
        method: 'DELETE',
        headers: { Authorization: 'Bearer should-not-forward' },
      })
    ).toEqual({
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID: 'egress_diag_stripped',
      operation: 'diagnostics',
      limit: 1,
    });
  });

  it('reads diagnostics only for the trusted frame and returns a bounded safe projection', async () => {
    const trustedSource = {} as MessageEventSource;
    const responses: unknown[] = [];
    const getDiagnostics = vi.fn(async (limit: number) => {
      expect(limit).toBe(1);
      return {
        generated_at: '2026-11-05T12:00:00Z',
        access_token: 'never-forward',
        bindings: [
          {
            egress_host: '64.8.252.252',
            owner_auth_id: 'account-a',
            hold_kind: 'overload_502',
            hold_until: '2026-11-05T12:00:30Z',
            updated_at: '2026-11-05T12:00:00Z',
            in_flight: 1,
            release_after_in_flight: true,
            proxy_url: 'http://user:pass@64.8.252.252:8080',
          },
          {
            egress_host: 'user:pass@invalid.example',
            owner_auth_id: 'must-not-render',
            updated_at: '2026-11-05T12:00:00Z',
            in_flight: 0,
          },
        ],
        events: [
          {
            at: '2026-11-05T12:00:00Z',
            event: 'owner_bound',
            reason: 'healthy',
            egress_host: '64.8.252.252',
            auth_id: 'account-a',
            hold_kind: 'overload_502',
            hold_until: '2026-11-05T12:00:30Z',
            refresh_token: 'never-forward',
          },
          {
            at: '2026-11-05T12:00:01Z',
            event: 'owner_released',
            egress_host: '64.8.252.252',
            auth_id: 'account-a',
          },
        ],
      };
    });
    const getConfig = vi.fn(async () => ({}));
    const patchConfig = vi.fn(async () => undefined);
    const handle = createRateLimitBridgeHost({
      expectedOrigin: 'https://cpa.kyon666.top',
      isActiveFrameSource: (source) => source === trustedSource,
      getConfig,
      patchConfig,
      getDiagnostics,
      reply: (_source, response) => responses.push(response),
    });

    await handle({
      data: {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'egress_diag_safe',
        operation: 'diagnostics',
        limit: 1,
      },
      origin: 'https://cpa.kyon666.top',
      source: trustedSource,
    });

    expect(getDiagnostics).toHaveBeenCalledTimes(1);
    expect(getConfig).not.toHaveBeenCalled();
    expect(patchConfig).not.toHaveBeenCalled();
    expect(responses).toEqual([
      {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'egress_diag_safe',
        operation: 'result',
        ok: true,
        diagnostics: {
          generatedAt: '2026-11-05T12:00:00Z',
          bindings: [
            {
              egressHost: '64.8.252.252',
              ownerAuthID: 'account-a',
              holdKind: 'overload_502',
              holdUntil: '2026-11-05T12:00:30Z',
              updatedAt: '2026-11-05T12:00:00Z',
              inFlight: 1,
              releaseAfterInFlight: true,
            },
          ],
          events: [
            {
              at: '2026-11-05T12:00:01Z',
              event: 'owner_released',
              egressHost: '64.8.252.252',
              authID: 'account-a',
            },
          ],
        },
      },
    ]);
    const serialized = JSON.stringify(responses);
    expect(serialized).not.toContain('proxy_url');
    expect(serialized).not.toContain('access_token');
    expect(serialized).not.toContain('refresh_token');
    expect(serialized).not.toContain('user:pass');
    expect(serialized).not.toContain('must-not-render');
  });

  it('does not invoke diagnostics for an untrusted frame', async () => {
    const trustedSource = {} as MessageEventSource;
    const getDiagnostics = vi.fn(async () => ({}));
    const handle = createRateLimitBridgeHost({
      expectedOrigin: 'https://cpa.kyon666.top',
      isActiveFrameSource: (source) => source === trustedSource,
      getConfig: async () => ({}),
      patchConfig: async () => undefined,
      getDiagnostics,
      reply: () => undefined,
    });

    await handle({
      data: {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'egress_diag_untrusted',
        operation: 'diagnostics',
        limit: 100,
      },
      origin: 'https://evil.invalid',
      source: trustedSource,
    });

    expect(getDiagnostics).not.toHaveBeenCalled();
  });});
