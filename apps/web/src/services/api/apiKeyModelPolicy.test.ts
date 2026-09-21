import { beforeEach, describe, expect, it, vi } from 'vitest';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    get: vi.fn(),
    put: vi.fn(),
  },
}));

vi.mock('./client', () => ({
  apiClient: {
    get: mocks.get,
    put: mocks.put,
  },
}));

import {
  apiKeyModelPolicyApi,
  parseApiKeyModelPolicy,
  type ApiKeyModelPolicyDocument,
} from './apiKeyModelPolicy';

const scope = 'a'.repeat(64);

const validPolicy = (): ApiKeyModelPolicyDocument => ({
  version: 1,
  enforcement_mode: 'enforce',
  unmanaged_key_behavior: 'deny',
  keys: {
    [scope]: {
      enabled: true,
      remark: 'Codex team',
      models: [{ public: 'codex-fast', target: 'gpt-5-codex' }],
    },
  },
});

beforeEach(() => {
  mocks.get.mockReset();
  mocks.put.mockReset();
});

describe('API key model policy parser', () => {
  it('normalizes a valid document without exposing response-only storage metadata', () => {
    expect(
      parseApiKeyModelPolicy({
        ...validPolicy(),
        storage_status: 'healthy',
      })
    ).toEqual(validPolicy());
  });

  it.each([
    ['missing version', { ...validPolicy(), version: undefined }],
    ['invalid enforcement mode', { ...validPolicy(), enforcement_mode: 'observe' }],
    ['invalid unmanaged behavior', { ...validPolicy(), unmanaged_key_behavior: 'reject' }],
    ['invalid scope', { ...validPolicy(), keys: { ABC: validPolicy().keys[scope] } }],
    ['extra top-level field', { ...validPolicy(), unexpected: true }],
    [
      'extra nested field',
      {
        ...validPolicy(),
        keys: {
          [scope]: { ...validPolicy().keys[scope], secret: 'must-reject' },
        },
      },
    ],
    [
      'extra rule field',
      {
        ...validPolicy(),
        keys: {
          [scope]: {
            ...validPolicy().keys[scope],
            models: [{ public: 'codex-fast', target: 'gpt-5-codex', method: 'POST' }],
          },
        },
      },
    ],
  ])('rejects %s', (_label, value) => {
    expect(() => parseApiKeyModelPolicy(value)).toThrow('Invalid API key model policy');
  });

  it('rejects model rules beyond the plugin contract', () => {
    const policy = validPolicy();
    policy.keys[scope].models = Array.from({ length: 101 }, (_, index) => ({
      public: `public-${index}`,
      target: `target-${index}`,
    }));

    expect(() => parseApiKeyModelPolicy(policy)).toThrow('Invalid API key model policy');
  });
});

describe('apiKeyModelPolicyApi', () => {
  it('uses only the fixed policy endpoint for reads', async () => {
    const policy = validPolicy();
    mocks.get.mockResolvedValue({ ...policy, storage_status: 'healthy' });

    await expect(apiKeyModelPolicyApi.getPolicy()).resolves.toEqual(policy);
    expect(mocks.get).toHaveBeenCalledWith('/plugins/api-key-model-policy/policy');
  });

  it('uses only the fixed policy endpoint for writes', async () => {
    const policy = validPolicy();
    mocks.put.mockResolvedValue({ ...policy, storage_status: 'healthy' });

    await expect(apiKeyModelPolicyApi.putPolicy(policy)).resolves.toEqual(policy);
    expect(mocks.put).toHaveBeenCalledWith('/plugins/api-key-model-policy/policy', policy);
  });

  it('rejects an invalid server response instead of coercing it', async () => {
    mocks.get.mockResolvedValue({ version: 1, keys: {} });

    await expect(apiKeyModelPolicyApi.getPolicy()).rejects.toThrow('Invalid API key model policy');
  });
});
