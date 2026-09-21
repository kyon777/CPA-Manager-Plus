import type { ApiKeyAlias } from '@/services/api/usageService';
import {
  parseApiKeyModelPolicy,
  type ApiKeyModelKeyPolicy,
  type ApiKeyModelPolicyDocument,
} from '@/services/api/apiKeyModelPolicy';
import { sha256RawTextHex } from '@/utils/apiKeyHash';
import { isRecord } from '@/utils/helpers';

export const API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL = 'cpamp.api-key-model-policy/v1';
export const API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID = 'api-key-model-policy';
export const API_KEY_MODEL_POLICY_BRIDGE_PARENT_ORIGIN_PARAM = 'cpamp_bridge_parent_origin';

const requestIDPattern = /^[A-Za-z0-9_-]{1,96}$/;
const callerScopePattern = /^[a-f0-9]{64}$/;
const nativeKeyPattern = /^sk-[0-9a-f]{96}$/;
const usageAliasHashPattern = /^[a-f0-9]{64}$/;

type BridgeOperation = 'load' | 'create' | 'reveal' | 'save_policy' | 'set_enabled' | 'delete';

export interface ApiKeyModelPolicyLoadRequest {
  protocol: typeof API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL;
  pluginID: typeof API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID;
  requestID: string;
  operation: 'load';
}

export interface ApiKeyModelPolicyCreateRequest {
  protocol: typeof API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL;
  pluginID: typeof API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID;
  requestID: string;
  operation: 'create';
}

export interface ApiKeyModelPolicyScopeRequest {
  protocol: typeof API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL;
  pluginID: typeof API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID;
  requestID: string;
  operation: 'reveal' | 'delete';
  scope: string;
}

export interface ApiKeyModelPolicySaveRequest {
  protocol: typeof API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL;
  pluginID: typeof API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID;
  requestID: string;
  operation: 'save_policy';
  policy: ApiKeyModelPolicyDocument;
}

export interface ApiKeyModelPolicySetEnabledRequest {
  protocol: typeof API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL;
  pluginID: typeof API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID;
  requestID: string;
  operation: 'set_enabled';
  scope: string;
  enabled: boolean;
}

export type ApiKeyModelPolicyBridgeRequest =
  | ApiKeyModelPolicyLoadRequest
  | ApiKeyModelPolicyCreateRequest
  | ApiKeyModelPolicyScopeRequest
  | ApiKeyModelPolicySaveRequest
  | ApiKeyModelPolicySetEnabledRequest;

export interface ApiKeyModelPolicyKeyRow {
  scope: string;
  masked: string;
  alias: string;
  configured: boolean;
  enabled: boolean;
  remark: string;
  models: Array<{ public: string; target: string }>;
}

export interface ApiKeyModelPolicyLoadData {
  policy: ApiKeyModelPolicyDocument;
  keys: ApiKeyModelPolicyKeyRow[];
  aliasAvailable: boolean;
}

export interface ApiKeyModelPolicyBridgeResponse {
  protocol: typeof API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL;
  pluginID: typeof API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID;
  requestID: string;
  operation: 'result';
  ok: boolean;
  data?:
    | ApiKeyModelPolicyLoadData
    | { policy: ApiKeyModelPolicyDocument }
    | { scope: string; plainText: string }
    | { scope: string };
  error?: string;
}

const hasOnlyKeys = (
  value: Record<string, unknown>,
  required: readonly string[],
  optional: readonly string[] = []
): boolean => {
  const allowed = new Set([...required, ...optional]);
  return (
    required.every((key) => Object.prototype.hasOwnProperty.call(value, key)) &&
    Object.keys(value).every((key) => allowed.has(key))
  );
};

const isValidRequestID = (value: unknown): value is string =>
  typeof value === 'string' && requestIDPattern.test(value);

const isValidScope = (value: unknown): value is string =>
  typeof value === 'string' && callerScopePattern.test(value);

const isValidPolicyInput = (value: unknown): value is ApiKeyModelPolicyDocument => {
  try {
    parseApiKeyModelPolicy(value, { allowStorageStatus: false });
    return true;
  } catch {
    return false;
  }
};

const clonePolicy = (policy: ApiKeyModelPolicyDocument): ApiKeyModelPolicyDocument => ({
  version: 1,
  enforcement_mode: policy.enforcement_mode,
  unmanaged_key_behavior: policy.unmanaged_key_behavior,
  keys: Object.fromEntries(
    Object.entries(policy.keys).map(([scope, entry]) => [
      scope,
      {
        enabled: entry.enabled,
        remark: entry.remark,
        models: entry.models.map((model) => ({ public: model.public, target: model.target })),
      },
    ])
  ),
});

export const parseApiKeyModelPolicyBridgeRequest = (
  value: unknown
): ApiKeyModelPolicyBridgeRequest | null => {
  if (!isRecord(value)) return null;
  if (
    value.protocol !== API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL ||
    value.pluginID !== API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID ||
    !isValidRequestID(value.requestID) ||
    typeof value.operation !== 'string'
  ) {
    return null;
  }

  const base = {
    protocol: API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL,
    pluginID: API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID,
    requestID: value.requestID,
  } as const;

  switch (value.operation as BridgeOperation) {
    case 'load':
      return hasOnlyKeys(value, ['protocol', 'pluginID', 'requestID', 'operation'])
        ? { ...base, operation: 'load' }
        : null;
    case 'create':
      return hasOnlyKeys(value, ['protocol', 'pluginID', 'requestID', 'operation'])
        ? { ...base, operation: 'create' }
        : null;
    case 'reveal':
    case 'delete':
      return hasOnlyKeys(value, ['protocol', 'pluginID', 'requestID', 'operation', 'scope']) &&
        isValidScope(value.scope)
        ? {
            ...base,
            operation: value.operation as 'reveal' | 'delete',
            scope: value.scope,
          }
        : null;
    case 'set_enabled':
      return hasOnlyKeys(value, [
        'protocol',
        'pluginID',
        'requestID',
        'operation',
        'scope',
        'enabled',
      ]) &&
        isValidScope(value.scope) &&
        typeof value.enabled === 'boolean'
        ? { ...base, operation: 'set_enabled', scope: value.scope, enabled: value.enabled }
        : null;
    case 'save_policy': {
      if (!hasOnlyKeys(value, ['protocol', 'pluginID', 'requestID', 'operation', 'policy'])) {
        return null;
      }
      if (!isValidPolicyInput(value.policy)) return null;
      return {
        ...base,
        operation: 'save_policy',
        policy: parseApiKeyModelPolicy(value.policy, { allowStorageStatus: false }),
      };
    }
    default:
      return null;
  }
};

export const parseTrustedApiKeyModelPolicyBridgeRequest = (
  value: unknown,
  trust: { sourceMatchesFrame: boolean; origin: string; expectedOrigin: string }
): ApiKeyModelPolicyBridgeRequest | null => {
  if (!trust.sourceMatchesFrame || !trust.expectedOrigin || trust.origin !== trust.expectedOrigin) {
    return null;
  }
  return parseApiKeyModelPolicyBridgeRequest(value);
};

export const callerScopeForNativeKey = (key: string): string =>
  sha256RawTextHex(`cli-proxy-api:caller-scope:v1\u0000${key.trim()}`);

export const usageAliasHashForNativeKey = (key: string): string => sha256RawTextHex(key);

const maskNativeKey = (key: string): string => {
  const value = key.trim();
  if (value.length <= 8) return '••••';
  return `${value.slice(0, 4)}…${value.slice(-4)}`;
};

const defaultNativeKeyGenerator = (): string => {
  const bytes = new Uint8Array(48);
  globalThis.crypto.getRandomValues(bytes);
  return `sk-${Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')}`;
};

const normalizeNativeKeys = (value: unknown): string[] => {
  if (!Array.isArray(value) || !value.every((key) => typeof key === 'string' && key.trim())) {
    throw new Error('invalid native key list');
  }
  return value.slice();
};

const uniqueNativeKeyIndexForScope = (keys: string[], scope: string): number => {
  const matches = keys
    .map((key, index) => (callerScopeForNativeKey(key) === scope ? index : -1))
    .filter((index) => index >= 0);
  return matches.length === 1 ? matches[0] : -1;
};

const policyScopesMatchNativeKeys = (
  policy: ApiKeyModelPolicyDocument,
  nativeKeys: string[]
): boolean => {
  const nativeScopes = new Set(nativeKeys.map(callerScopeForNativeKey));
  return Object.keys(policy.keys).every((scope) => nativeScopes.has(scope));
};

const buildRows = (
  nativeKeys: string[],
  policy: ApiKeyModelPolicyDocument,
  aliases: ApiKeyAlias[]
): ApiKeyModelPolicyKeyRow[] => {
  const aliasByHash = new Map<string, string>();
  for (const item of aliases) {
    if (
      !isRecord(item) ||
      typeof item.apiKeyHash !== 'string' ||
      !usageAliasHashPattern.test(item.apiKeyHash) ||
      typeof item.alias !== 'string'
    ) {
      continue;
    }
    const alias = item.alias.trim();
    if (alias) aliasByHash.set(item.apiKeyHash, alias);
  }

  return nativeKeys.map((key) => {
    const scope = callerScopeForNativeKey(key);
    const configured = policy.keys[scope];
    return {
      scope,
      masked: maskNativeKey(key),
      alias: aliasByHash.get(usageAliasHashForNativeKey(key)) || '',
      configured: Boolean(configured),
      enabled: configured?.enabled ?? true,
      remark: configured?.remark ?? '',
      models:
        configured?.models.map((model) => ({ public: model.public, target: model.target })) ?? [],
    };
  });
};

export interface ApiKeyModelPolicyBridgeHostEvent {
  data: unknown;
  origin: string;
  source: MessageEventSource | null;
}

export interface ApiKeyModelPolicyBridgeHostOptions {
  expectedOrigin: string;
  isActiveFrameSource: (source: MessageEventSource | null) => boolean;
  listNativeKeys: () => Promise<string[]>;
  replaceNativeKeys: (keys: string[]) => Promise<unknown>;
  getPolicy: () => Promise<ApiKeyModelPolicyDocument>;
  putPolicy: (policy: ApiKeyModelPolicyDocument) => Promise<ApiKeyModelPolicyDocument>;
  getAliases: () => Promise<ApiKeyAlias[]>;
  reply: (source: MessageEventSource | null, response: ApiKeyModelPolicyBridgeResponse) => void;
  generateKey?: () => string;
}

const successResponse = (
  request: ApiKeyModelPolicyBridgeRequest,
  data:
    | ApiKeyModelPolicyLoadData
    | { policy: ApiKeyModelPolicyDocument }
    | { scope: string; plainText: string }
    | { scope: string }
): ApiKeyModelPolicyBridgeResponse => ({
  protocol: API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL,
  pluginID: API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID,
  requestID: request.requestID,
  operation: 'result',
  ok: true,
  data,
});

const failureResponse = (
  request: ApiKeyModelPolicyBridgeRequest,
  error: string
): ApiKeyModelPolicyBridgeResponse => ({
  protocol: API_KEY_MODEL_POLICY_BRIDGE_PROTOCOL,
  pluginID: API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID,
  requestID: request.requestID,
  operation: 'result',
  ok: false,
  error,
});

const normalizedPolicyFromHost = async (
  getPolicy: () => Promise<ApiKeyModelPolicyDocument>
): Promise<ApiKeyModelPolicyDocument> =>
  parseApiKeyModelPolicy(await getPolicy(), { allowStorageStatus: false });

const normalizedNativeKeysFromHost = async (
  listNativeKeys: () => Promise<string[]>
): Promise<string[]> => normalizeNativeKeys(await listNativeKeys());

const createPolicyEntry = (): ApiKeyModelKeyPolicy => ({ enabled: true, remark: '', models: [] });

const operationFailure = (operation: BridgeOperation): string => {
  switch (operation) {
    case 'load':
      return '无法读取 API Key 模型策略';
    case 'create':
      return '创建失败';
    case 'reveal':
      return '无法显示 API Key';
    case 'save_policy':
    case 'set_enabled':
      return '策略保存失败';
    case 'delete':
      return '删除失败';
  }
};

const nativeKeyNotFoundFailure = 'native_key_not_found';

export const createApiKeyModelPolicyBridgeHost = (options: ApiKeyModelPolicyBridgeHostOptions) => {
  let mutationTail: Promise<void> = Promise.resolve();

  const runMutation = <T>(operation: () => Promise<T>): Promise<T> => {
    const result = mutationTail.then(operation, operation);
    mutationTail = result.then(
      () => undefined,
      () => undefined
    );
    return result;
  };

  const load = async (): Promise<ApiKeyModelPolicyLoadData> => {
    const [nativeKeys, policy] = await Promise.all([
      normalizedNativeKeysFromHost(options.listNativeKeys),
      normalizedPolicyFromHost(options.getPolicy),
    ]);
    if (!policyScopesMatchNativeKeys(policy, nativeKeys)) {
      throw new Error('policy contains a non-native key');
    }

    let aliases: ApiKeyAlias[] = [];
    let aliasAvailable = true;
    try {
      const value = await options.getAliases();
      aliases = Array.isArray(value) ? value : [];
    } catch {
      aliasAvailable = false;
    }

    return {
      policy,
      keys: buildRows(nativeKeys, policy, aliases),
      aliasAvailable,
    };
  };

  const create = async (
    request: ApiKeyModelPolicyCreateRequest
  ): Promise<ApiKeyModelPolicyBridgeResponse> => {
    const nativeBefore = await normalizedNativeKeysFromHost(options.listNativeKeys);
    const policyBefore = await normalizedPolicyFromHost(options.getPolicy);
    if (!policyScopesMatchNativeKeys(policyBefore, nativeBefore)) {
      return failureResponse(request, '策略包含不存在的原生 Key');
    }

    const generator = options.generateKey ?? defaultNativeKeyGenerator;
    let generated = '';
    let generatedScope = '';
    for (let attempt = 0; attempt < 5; attempt += 1) {
      const candidate = generator();
      if (!nativeKeyPattern.test(candidate) || nativeBefore.includes(candidate)) continue;
      const candidateScope = callerScopeForNativeKey(candidate);
      if (policyBefore.keys[candidateScope]) continue;
      generated = candidate;
      generatedScope = candidateScope;
      break;
    }
    if (!generated) return failureResponse(request, '创建失败');

    try {
      await options.replaceNativeKeys([...nativeBefore, generated]);
    } catch {
      return failureResponse(request, '创建失败');
    }

    const nextPolicy = clonePolicy(policyBefore);
    nextPolicy.keys[generatedScope] = createPolicyEntry();
    try {
      const persisted = await options.putPolicy(nextPolicy);
      parseApiKeyModelPolicy(persisted, { allowStorageStatus: false });
      return successResponse(request, { scope: generatedScope, plainText: generated });
    } catch {
      try {
        await options.replaceNativeKeys(nativeBefore);
      } catch {
        return failureResponse(request, '创建失败，且原生 Key 回滚失败');
      }
      return failureResponse(request, '创建失败');
    }
  };

  const reveal = async (
    request: ApiKeyModelPolicyScopeRequest
  ): Promise<ApiKeyModelPolicyBridgeResponse> => {
    const nativeKeys = await normalizedNativeKeysFromHost(options.listNativeKeys);
    const index = uniqueNativeKeyIndexForScope(nativeKeys, request.scope);
    if (index < 0) return failureResponse(request, nativeKeyNotFoundFailure);
    return successResponse(request, { scope: request.scope, plainText: nativeKeys[index] });
  };

  const savePolicy = async (
    request: ApiKeyModelPolicySaveRequest
  ): Promise<ApiKeyModelPolicyBridgeResponse> => {
    const nativeKeys = await normalizedNativeKeysFromHost(options.listNativeKeys);
    if (!policyScopesMatchNativeKeys(request.policy, nativeKeys)) {
      return failureResponse(request, '策略包含不存在的原生 Key');
    }
    try {
      const persisted = await options.putPolicy(clonePolicy(request.policy));
      const policy = parseApiKeyModelPolicy(persisted, { allowStorageStatus: false });
      return successResponse(request, { policy });
    } catch {
      return failureResponse(request, '策略保存失败');
    }
  };

  const setEnabled = async (
    request: ApiKeyModelPolicySetEnabledRequest
  ): Promise<ApiKeyModelPolicyBridgeResponse> => {
    const nativeKeys = await normalizedNativeKeysFromHost(options.listNativeKeys);
    if (uniqueNativeKeyIndexForScope(nativeKeys, request.scope) < 0) {
      return failureResponse(request, nativeKeyNotFoundFailure);
    }
    try {
      const policy = await normalizedPolicyFromHost(options.getPolicy);
      if (!policyScopesMatchNativeKeys(policy, nativeKeys)) {
        return failureResponse(request, '策略包含不存在的原生 Key');
      }
      const nextPolicy = clonePolicy(policy);
      nextPolicy.keys[request.scope] = {
        ...(nextPolicy.keys[request.scope] ?? createPolicyEntry()),
        enabled: request.enabled,
      };
      const persisted = await options.putPolicy(nextPolicy);
      return successResponse(request, {
        policy: parseApiKeyModelPolicy(persisted, { allowStorageStatus: false }),
      });
    } catch {
      return failureResponse(request, '策略保存失败');
    }
  };

  const remove = async (
    request: ApiKeyModelPolicyScopeRequest
  ): Promise<ApiKeyModelPolicyBridgeResponse> => {
    const nativeBefore = await normalizedNativeKeysFromHost(options.listNativeKeys);
    const index = uniqueNativeKeyIndexForScope(nativeBefore, request.scope);
    if (index < 0) return failureResponse(request, nativeKeyNotFoundFailure);
    const policyBefore = await normalizedPolicyFromHost(options.getPolicy);
    if (!policyScopesMatchNativeKeys(policyBefore, nativeBefore)) {
      return failureResponse(request, '策略包含不存在的原生 Key');
    }

    const nativeAfter = nativeBefore.filter((_key, keyIndex) => keyIndex !== index);
    try {
      await options.replaceNativeKeys(nativeAfter);
    } catch {
      return failureResponse(request, '删除失败');
    }

    const nextPolicy = clonePolicy(policyBefore);
    delete nextPolicy.keys[request.scope];
    try {
      const persisted = await options.putPolicy(nextPolicy);
      parseApiKeyModelPolicy(persisted, { allowStorageStatus: false });
      return successResponse(request, { scope: request.scope });
    } catch {
      try {
        await options.replaceNativeKeys(nativeBefore);
      } catch {
        return failureResponse(request, '删除失败，且原生 Key 回滚失败');
      }
      return failureResponse(request, '删除失败');
    }
  };

  const dispatch = async (
    request: ApiKeyModelPolicyBridgeRequest
  ): Promise<ApiKeyModelPolicyBridgeResponse> => {
    try {
      switch (request.operation) {
        case 'load':
          return successResponse(request, await load());
        case 'create':
          return await create(request);
        case 'reveal':
          return await reveal(request);
        case 'save_policy':
          return await savePolicy(request);
        case 'set_enabled':
          return await setEnabled(request);
        case 'delete':
          return await remove(request);
      }
    } catch {
      return failureResponse(request, operationFailure(request.operation));
    }
  };

  return async (event: ApiKeyModelPolicyBridgeHostEvent): Promise<void> => {
    const request = parseTrustedApiKeyModelPolicyBridgeRequest(event.data, {
      sourceMatchesFrame: options.isActiveFrameSource(event.source),
      origin: event.origin,
      expectedOrigin: options.expectedOrigin,
    });
    if (!request) return;

    const isMutation =
      request.operation === 'create' ||
      request.operation === 'save_policy' ||
      request.operation === 'set_enabled' ||
      request.operation === 'delete';
    const response = isMutation
      ? await runMutation(() => dispatch(request))
      : await dispatch(request);
    options.reply(event.source, response);
  };
};

const isHTTPOrigin = (url: URL): boolean => url.protocol === 'http:' || url.protocol === 'https:';

export const appendApiKeyModelPolicyBridgeParentOrigin = (
  source: string,
  parentOrigin: string
): string => {
  try {
    const parentURL = new URL(parentOrigin);
    const resourceURL = new URL(source, parentURL.origin);
    if (!isHTTPOrigin(parentURL) || !isHTTPOrigin(resourceURL)) return source;
    resourceURL.searchParams.set(API_KEY_MODEL_POLICY_BRIDGE_PARENT_ORIGIN_PARAM, parentURL.origin);
    return resourceURL.toString();
  } catch {
    return source;
  }
};

export const resolveApiKeyModelPolicyBridgeFrameOrigin = (
  source: string,
  parentOrigin: string
): string => {
  try {
    const resourceURL = new URL(source, parentOrigin);
    return isHTTPOrigin(resourceURL) ? resourceURL.origin : '';
  } catch {
    return '';
  }
};
