import { describe, expect, it } from 'vitest';
import {
  RATE_LIMIT_BRIDGE_PROTOCOL,
  appendRateLimitBridgeParentOrigin,
  buildRateLimitConfigPatch,
  createRateLimitBridgeHost,
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
      settings: { enabled: false, rpm: 30, minInterval: '10s', maxQueueWait: '35s', maxPendingRequests: 500 },
    });
    expect(save).toMatchObject({
      operation: 'save',
      settings: { rpm: 30, minInterval: '10s', maxQueueWait: '35s', maxPendingRequests: 500 },
    });
    if (!save || save.operation !== 'save') throw new Error('expected save request');

    expect(buildRateLimitConfigPatch(save)).toEqual({
      enabled: false,
      default: { rpm: 30, min_interval: '10s' },
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
        settings: { enabled: true, rpm: -1, minInterval: '10s', maxQueueWait: '35s', maxPendingRequests: 500 },
      },
      {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'save_1',
        operation: 'save',
        settings: { enabled: true, rpm: 1, minInterval: '-1s', maxQueueWait: '35s', maxPendingRequests: 500 },
      },
      {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: 'per-auth-rate-limit',
        requestID: 'save_1',
        operation: 'save',
        settings: { enabled: true, rpm: 1, minInterval: '1s', maxQueueWait: '35s', maxPendingRequests: -1 },
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
        default: { rpm: 8, min_interval: '0s' },
        max_queue_wait: '35s',
        max_pending_requests: 500,
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
        settings: { enabled: true, rpm: 6, minInterval: '10s', maxQueueWait: '1m', maxPendingRequests: 42 },
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
        default: { rpm: 6, min_interval: '10s' },
        max_queue_wait: '1m',
        max_pending_requests: 42,
        accounts: null,
      },
    ]);
    expect(sent).toEqual([
      expect.objectContaining({ requestID: 'save_2', operation: 'result', ok: true }),
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
});
