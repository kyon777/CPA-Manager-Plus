import { apiClient } from './client';
import { isRecord } from '@/utils/helpers';

export type ApiKeyModelPolicyEnforcementMode = 'audit' | 'enforce';
export type ApiKeyModelPolicyUnmanagedBehavior = 'allow' | 'deny';

export interface ApiKeyModelRule {
  public: string;
  target: string;
}

export interface ApiKeyModelKeyPolicy {
  enabled: boolean;
  remark: string;
  models: ApiKeyModelRule[];
}

export interface ApiKeyModelPolicyDocument {
  version: 1;
  enforcement_mode: ApiKeyModelPolicyEnforcementMode;
  unmanaged_key_behavior: ApiKeyModelPolicyUnmanagedBehavior;
  keys: Record<string, ApiKeyModelKeyPolicy>;
}

export const API_KEY_MODEL_POLICY_PATH = '/plugins/api-key-model-policy/policy';

const INVALID_POLICY_MESSAGE = 'Invalid API key model policy';
const MAX_POLICY_KEYS = 10_000;
const MAX_MODELS_PER_KEY = 100;
const MAX_REMARK_CODE_POINTS = 256;
const MAX_MODEL_NAME_CODE_POINTS = 128;
const callerScopePattern = /^[a-f0-9]{64}$/;

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

const codePointLength = (value: string): number => Array.from(value).length;

const parsePolicyModelRule = (value: unknown): ApiKeyModelRule | null => {
  if (!isRecord(value) || !hasOnlyKeys(value, ['public', 'target'])) return null;
  if (typeof value.public !== 'string' || typeof value.target !== 'string') return null;

  const publicName = value.public.trim();
  const targetName = value.target.trim();
  if (
    !publicName ||
    !targetName ||
    codePointLength(publicName) > MAX_MODEL_NAME_CODE_POINTS ||
    codePointLength(targetName) > MAX_MODEL_NAME_CODE_POINTS
  ) {
    return null;
  }

  return { public: publicName, target: targetName };
};

const parseKeyPolicy = (value: unknown): ApiKeyModelKeyPolicy | null => {
  if (!isRecord(value) || !hasOnlyKeys(value, ['enabled', 'remark', 'models'])) return null;
  if (typeof value.enabled !== 'boolean' || typeof value.remark !== 'string') return null;
  if (!Array.isArray(value.models) || value.models.length > MAX_MODELS_PER_KEY) return null;

  const remark = value.remark.trim();
  if (codePointLength(remark) > MAX_REMARK_CODE_POINTS) return null;

  const models: ApiKeyModelRule[] = [];
  const publicNames = new Set<string>();
  for (const rawRule of value.models) {
    const rule = parsePolicyModelRule(rawRule);
    if (!rule || publicNames.has(rule.public)) return null;
    publicNames.add(rule.public);
    models.push(rule);
  }

  return {
    enabled: value.enabled,
    remark,
    models,
  };
};

export interface ParseApiKeyModelPolicyOptions {
  allowStorageStatus?: boolean;
}

/**
 * Parse the plugin's JSON document without coercing untrusted response data.
 * `storage_status` is accepted only for the CPA response envelope and is
 * deliberately omitted from the returned policy document.
 */
export const parseApiKeyModelPolicy = (
  value: unknown,
  options: ParseApiKeyModelPolicyOptions = {}
): ApiKeyModelPolicyDocument => {
  if (!isRecord(value)) throw new Error(INVALID_POLICY_MESSAGE);

  const allowStorageStatus = options.allowStorageStatus !== false;
  const optionalKeys = allowStorageStatus ? ['storage_status'] : [];
  if (
    !hasOnlyKeys(
      value,
      ['version', 'enforcement_mode', 'unmanaged_key_behavior', 'keys'],
      optionalKeys
    )
  ) {
    throw new Error(INVALID_POLICY_MESSAGE);
  }
  if (
    value.version !== 1 ||
    (value.enforcement_mode !== 'audit' && value.enforcement_mode !== 'enforce') ||
    (value.unmanaged_key_behavior !== 'allow' && value.unmanaged_key_behavior !== 'deny')
  ) {
    throw new Error(INVALID_POLICY_MESSAGE);
  }
  if (allowStorageStatus && Object.prototype.hasOwnProperty.call(value, 'storage_status')) {
    if (value.storage_status !== 'healthy' && value.storage_status !== 'unavailable') {
      throw new Error(INVALID_POLICY_MESSAGE);
    }
  }
  if (!isRecord(value.keys) || Object.keys(value.keys).length > MAX_POLICY_KEYS) {
    throw new Error(INVALID_POLICY_MESSAGE);
  }

  const keys: Record<string, ApiKeyModelKeyPolicy> = {};
  for (const [scope, rawPolicy] of Object.entries(value.keys)) {
    if (!callerScopePattern.test(scope)) throw new Error(INVALID_POLICY_MESSAGE);
    const policy = parseKeyPolicy(rawPolicy);
    if (!policy) throw new Error(INVALID_POLICY_MESSAGE);
    keys[scope] = policy;
  }

  return {
    version: 1,
    enforcement_mode: value.enforcement_mode,
    unmanaged_key_behavior: value.unmanaged_key_behavior,
    keys,
  };
};

export const apiKeyModelPolicyApi = {
  async getPolicy(): Promise<ApiKeyModelPolicyDocument> {
    return parseApiKeyModelPolicy(await apiClient.get<unknown>(API_KEY_MODEL_POLICY_PATH));
  },

  async putPolicy(document: ApiKeyModelPolicyDocument): Promise<ApiKeyModelPolicyDocument> {
    const normalized = parseApiKeyModelPolicy(document, { allowStorageStatus: false });
    const response = await apiClient.put<unknown>(API_KEY_MODEL_POLICY_PATH, normalized);
    return parseApiKeyModelPolicy(response);
  },
};
