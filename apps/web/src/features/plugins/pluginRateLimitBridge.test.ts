import { describe, expect, it } from 'vitest';
import {
  RATE_LIMIT_BRIDGE_PROTOCOL,
  appendRateLimitBridgeParentOrigin,
  buildRateLimitConfigPatch,
  createRateLimitBridgeHost,
  normalizeRateLimitBridgeSettings,
  parseRateLimitBridgeRequest,
  parseTrustedRateLimitBridgeRequest,
  resolveRateLimitBridgeFrameOrigin,
} from './pluginRateLimitBridge';

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
      }),
    });
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
});
