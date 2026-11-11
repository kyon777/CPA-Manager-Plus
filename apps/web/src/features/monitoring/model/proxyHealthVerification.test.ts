import { describe, expect, it } from 'vitest';
import type { CodexInspectionResultItem } from '@/features/monitoring/codexInspection';
import type { CredentialProxyExitIPTargetRequest } from '@/services/api/usageService';
import {
  applyProxyHealthVerificationResults,
  collectProxyHealthVerificationGroups,
  hasValidCurrentProxyHealthVerification,
} from './proxyHealthVerification';

const createResult = (
  key: string,
  overrides: Partial<CodexInspectionResultItem> = {}
): CodexInspectionResultItem =>
  ({
    key,
    fileName: `${key}.json`,
    displayAccount: `${key}@example.com`,
    accountSnapshot: `${key}@example.com`,
    authIndex: key,
    accountId: null,
    provider: 'codex',
    disabled: false,
    autoRecoverOwned: false,
    status: '',
    state: '',
    raw: {},
    action: 'keep',
    actionReason: '',
    statusCode: 200,
    usedPercent: null,
    isQuota: false,
    autoRecoverEligible: false,
    error: '',
    ...overrides,
  }) as CodexInspectionResultItem;

const createTarget = (key: string): CredentialProxyExitIPTargetRequest => ({
  clientKey: key,
  fileName: `${key}.json`,
  provider: 'codex',
  authIndex: key,
  accountEmail: `${key}@example.com`,
  runtimeId: `runtime-${key}`,
});

describe('proxy health verification', () => {
  it('groups disabled HTTP-200 credentials by current proxy and fans a direct failure back to every member', () => {
    const disabledHttp200 = createResult('historic-disabled', { disabled: true, statusCode: 200 });
    const enabledHttp200 = createResult('current-enabled', { statusCode: 200 });
    const groups = collectProxyHealthVerificationGroups([
      { item: disabledHttp200, proxyURL: 'http://shared.example:8080/', target: createTarget(disabledHttp200.key) },
      { item: enabledHttp200, proxyURL: 'http://shared.example:8080', target: createTarget(enabledHttp200.key) },
    ]);

    expect(groups).toHaveLength(1);
    expect(groups[0]?.representative.clientKey).toBe('historic-disabled');
    expect(groups[0]?.memberKeys).toEqual(['historic-disabled', 'current-enabled']);

    const updated = applyProxyHealthVerificationResults(
      [disabledHttp200, enabledHttp200],
      groups,
      [{ clientKey: 'historic-disabled', outcome: 'proxy_failed', reason: 'proxy_auth_407' }],
      1_700_000_000_000
    );

    expect(updated.map((item) => [item.key, item.proxySuspect, item.proxyVerificationOutcome])).toEqual([
      ['historic-disabled', true, 'proxy_failed'],
      ['current-enabled', true, 'proxy_failed'],
    ]);
    expect(updated[0]?.proxyVerificationFingerprint).toMatch(/^[a-f0-9]{64}$/);
    expect(updated[0]?.proxyVerificationFingerprint).toBe(updated[1]?.proxyVerificationFingerprint);
    expect(updated[0]?.proxyVerificationReason).toBe('proxy_auth_407');
  });

  it('keeps a non-failing direct outcome out of the proxy-abnormal filter and invalidates proof when the current proxy changes', () => {
    const row = createResult('one');
    const groups = collectProxyHealthVerificationGroups([
      { item: row, proxyURL: 'http://healthy.example:8080', target: createTarget(row.key) },
    ]);
    const [updated] = applyProxyHealthVerificationResults(
      [row],
      groups,
      [{ clientKey: 'one', outcome: 'healthy', exitIp: '203.0.113.8' }],
      1_700_000_000_001
    );

    expect(updated?.proxySuspect).toBe(false);
    expect(updated?.proxyVerificationOutcome).toBe('healthy');
    expect(hasValidCurrentProxyHealthVerification(updated!, 'http://healthy.example:8080/')).toBe(true);
    expect(hasValidCurrentProxyHealthVerification(updated!, 'http://changed.example:8080')).toBe(false);
    expect(collectProxyHealthVerificationGroups([
      { item: updated!, proxyURL: 'http://changed.example:8080', target: createTarget(row.key) },
    ])).toHaveLength(1);
  });

  it('does not turn inconclusive proof into a proxy anomaly', () => {
    const row = createResult('one', { proxySuspect: true });
    const groups = collectProxyHealthVerificationGroups([
      { item: row, proxyURL: 'http://inconclusive.example:8080', target: createTarget(row.key) },
    ]);
    const [updated] = applyProxyHealthVerificationResults(
      [row],
      groups,
      [{ clientKey: 'one', outcome: 'inconclusive', reason: 'ip_echo_unavailable' }],
      1_700_000_000_002
    );

    expect(updated?.proxySuspect).toBe(false);
    expect(updated?.proxyVerificationOutcome).toBe('inconclusive');
  });
});
