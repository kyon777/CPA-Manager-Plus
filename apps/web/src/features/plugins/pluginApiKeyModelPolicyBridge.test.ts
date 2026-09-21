import { describe, expect, it, vi } from 'vitest';
import type { ApiKeyAlias } from '@/services/api/usageService';
import type { ApiKeyModelPolicyDocument, ApiKeyModelRule } from '@/services/api/apiKeyModelPolicy';
import {
  API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID,
  API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL,
  callerScopeForNativeKey,
  createApiKeyModelPolicyBridgeHost,
  parseApiKeyModelPolicyBridgeRequest,
  parseTrustedApiKeyModelPolicyBridgeRequest,
  usageAliasHashForNativeKey,
  type ApiKeyModelPolicyBridgeHostEvent,
} from './pluginApiKeyModelPolicyBridge';

const nativeKey = 'sk-native-one';
const scope = callerScopeForNativeKey(nativeKey);

const basePolicy = (): ApiKeyModelPolicyDocument => ({
  version: 1,
  enforcement_mode: 'enforce',
  unmanaged_key_behavior: 'deny',
  keys: {
    [scope]: {
      enabled: true,
      remark: 'primary',
      models: [{ public: 'codex-fast', target: 'gpt-5-codex' }],
    },
  },
});

const makeEvent = (
  data: unknown,
  source: MessageEventSource,
  origin = 'https://cpam.kyon666.top'
): ApiKeyModelPolicyBridgeHostEvent => ({ data, source, origin });

const loadRequest = {
  protocol: API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL,
  pluginID: API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID,
  requestID: 'load_1',
  operation: 'load' as const,
};

describe('API key model policy bridge input boundary', () => {
  it('accepts only fixed operations and rejects proxy-shaped messages', () => {
    expect(parseApiKeyModelPolicyBridgeRequest(loadRequest)).toEqual(loadRequest);
    expect(
      parseApiKeyModelPolicyBridgeRequest({
        ...loadRequest,
        operation: 'fetch',
        url: 'http://evil.invalid',
        method: 'GET',
        headers: { Authorization: 'Bearer leaked' },
      })
    ).toBeNull();
    expect(
      parseApiKeyModelPolicyBridgeRequest({
        ...loadRequest,
        unexpected: true,
      })
    ).toBeNull();
    expect(
      parseApiKeyModelPolicyBridgeRequest({
        ...loadRequest,
        requestID: 'contains spaces',
      })
    ).toBeNull();
  });

  it('requires the active iframe source and exact frame origin', () => {
    const trustedSource = {} as MessageEventSource;
    expect(
      parseTrustedApiKeyModelPolicyBridgeRequest(loadRequest, {
        sourceMatchesFrame: true,
        origin: 'https://cpam.kyon666.top',
        expectedOrigin: 'https://cpam.kyon666.top',
      })
    ).toEqual(loadRequest);
    expect(
      parseTrustedApiKeyModelPolicyBridgeRequest(loadRequest, {
        sourceMatchesFrame: false,
        origin: 'https://cpam.kyon666.top',
        expectedOrigin: 'https://cpam.kyon666.top',
      })
    ).toBeNull();
    expect(
      parseTrustedApiKeyModelPolicyBridgeRequest(loadRequest, {
        sourceMatchesFrame: true,
        origin: 'https://evil.example',
        expectedOrigin: 'https://cpam.kyon666.top',
      })
    ).toBeNull();
    expect(trustedSource).toBeDefined();
  });

  it('limits scope, Unicode remarks, and model-rule input', () => {
    expect(
      parseApiKeyModelPolicyBridgeRequest({
        ...loadRequest,
        operation: 'reveal',
        scope: 'A'.repeat(64),
      })
    ).toBeNull();

    const oversizedRemark = '界'.repeat(257);
    expect(
      parseApiKeyModelPolicyBridgeRequest({
        ...loadRequest,
        operation: 'save_policy',
        policy: {
          ...basePolicy(),
          keys: {
            [scope]: { ...basePolicy().keys[scope], remark: oversizedRemark },
          },
        },
      })
    ).toBeNull();

    const tooManyRules: ApiKeyModelRule[] = Array.from({ length: 101 }, (_, index) => ({
      public: `public-${index}`,
      target: `target-${index}`,
    }));
    expect(
      parseApiKeyModelPolicyBridgeRequest({
        ...loadRequest,
        operation: 'save_policy',
        policy: {
          ...basePolicy(),
          keys: { [scope]: { enabled: true, remark: '', models: tooManyRules } },
        },
      })
    ).toBeNull();
  });
});

describe('API key model policy bridge behavior', () => {
  it('uses the documented caller-scope and usage-alias hash domains', () => {
    expect(callerScopeForNativeKey(' sk-test ')).toBe(
      'c512aa71887f77cd6b915a60aed04e1d0de0ed0721f041e51d1002402b3901db'
    );
    expect(usageAliasHashForNativeKey('sk-test')).toBe(
      'f3abf2a6cc4f00987743db5f544ba345b4899ae31f326d8ee9c4816de153c9e0'
    );
  });

  it('loads masked native keys with aliases but never sends plaintext or management credentials', async () => {
    const source = {} as MessageEventSource;
    const sent: unknown[] = [];
    const aliases: ApiKeyAlias[] = [
      { apiKeyHash: usageAliasHashForNativeKey(nativeKey), alias: 'Primary key' },
    ];
    const handle = createApiKeyModelPolicyBridgeHost({
      expectedOrigin: 'https://cpam.kyon666.top',
      isActiveFrameSource: (candidate) => candidate === source,
      listNativeKeys: async () => [nativeKey],
      replaceNativeKeys: async () => undefined,
      getPolicy: async () => basePolicy(),
      putPolicy: async (policy) => policy,
      getAliases: async () => aliases,
      reply: (_target, response) => sent.push(response),
    });

    await handle(makeEvent(loadRequest, source));

    const response = sent[0] as {
      ok: boolean;
      data?: { keys?: Array<Record<string, unknown>>; aliasAvailable?: boolean };
    };
    expect(response.ok).toBe(true);
    expect(response.data?.aliasAvailable).toBe(true);
    expect(response.data?.keys).toEqual([
      expect.objectContaining({
        scope,
        alias: 'Primary key',
        configured: true,
        enabled: true,
      }),
    ]);
    expect(JSON.stringify(response)).not.toContain(nativeKey);
    expect(JSON.stringify(response)).not.toContain('managementKey');
  });

  it('keeps loading when the optional usage-alias service is unavailable', async () => {
    const source = {} as MessageEventSource;
    const sent: unknown[] = [];
    const handle = createApiKeyModelPolicyBridgeHost({
      expectedOrigin: 'https://cpam.kyon666.top',
      isActiveFrameSource: (candidate) => candidate === source,
      listNativeKeys: async () => [nativeKey],
      replaceNativeKeys: async () => undefined,
      getPolicy: async () => basePolicy(),
      putPolicy: async (policy) => policy,
      getAliases: async () => {
        throw new Error('usage unavailable');
      },
      reply: (_target, response) => sent.push(response),
    });

    await handle(makeEvent(loadRequest, source));

    expect(sent[0]).toMatchObject({ ok: true, data: { aliasAvailable: false } });
  });

  it('rolls back the complete native key list when create policy persistence fails', async () => {
    const source = {} as MessageEventSource;
    const sent: unknown[] = [];
    const nativeLists: string[][] = [];
    const handle = createApiKeyModelPolicyBridgeHost({
      expectedOrigin: 'https://cpam.kyon666.top',
      isActiveFrameSource: (candidate) => candidate === source,
      listNativeKeys: async () => [nativeKey],
      replaceNativeKeys: async (keys) => {
        nativeLists.push([...keys]);
      },
      getPolicy: async () => basePolicy(),
      putPolicy: async () => {
        throw new Error('policy write failed');
      },
      getAliases: async () => [],
      reply: (_target, response) => sent.push(response),
      generateKey: () => 'sk-' + 'c'.repeat(96),
    });

    await handle(
      makeEvent(
        {
          protocol: API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL,
          pluginID: API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID,
          requestID: 'create_1',
          operation: 'create',
        },
        source
      )
    );

    expect(nativeLists[0]).toEqual([nativeKey, 'sk-' + 'c'.repeat(96)]);
    expect(nativeLists[1]).toEqual([nativeKey]);
    expect(sent[0]).toMatchObject({
      ok: false,
      error: '创建失败',
    });
    expect(JSON.stringify(sent[0])).not.toContain('sk-' + 'c'.repeat(96));
  });

  it('rolls back the native key list when delete policy persistence fails', async () => {
    const source = {} as MessageEventSource;
    const sent: unknown[] = [];
    const nativeLists: string[][] = [];
    const handle = createApiKeyModelPolicyBridgeHost({
      expectedOrigin: 'https://cpam.kyon666.top',
      isActiveFrameSource: (candidate) => candidate === source,
      listNativeKeys: async () => [nativeKey],
      replaceNativeKeys: async (keys) => {
        nativeLists.push([...keys]);
      },
      getPolicy: async () => basePolicy(),
      putPolicy: async () => {
        throw new Error('policy write failed');
      },
      getAliases: async () => [],
      reply: (_target, response) => sent.push(response),
    });

    await handle(
      makeEvent(
        {
          protocol: API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL,
          pluginID: API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID,
          requestID: 'delete_1',
          operation: 'delete',
          scope,
        },
        source
      )
    );

    expect(nativeLists).toEqual([[], [nativeKey]]);
    expect(sent[0]).toMatchObject({ ok: false, error: '删除失败' });
  });

  it('does not forward requests from an inactive iframe to management capabilities', async () => {
    const source = {} as MessageEventSource;
    const otherSource = {} as MessageEventSource;
    const listNativeKeys = vi.fn(async () => [nativeKey]);
    const sent: unknown[] = [];
    const handle = createApiKeyModelPolicyBridgeHost({
      expectedOrigin: 'https://cpam.kyon666.top',
      isActiveFrameSource: (candidate) => candidate === source,
      listNativeKeys,
      replaceNativeKeys: async () => undefined,
      getPolicy: async () => basePolicy(),
      putPolicy: async (policy) => policy,
      getAliases: async () => [],
      reply: (_target, response) => sent.push(response),
    });

    await handle(makeEvent(loadRequest, otherSource));
    await handle(makeEvent(loadRequest, source, 'https://evil.example'));

    expect(listNativeKeys).not.toHaveBeenCalled();
    expect(sent).toHaveLength(0);
  });
});
