import { describe, expect, it } from 'vitest';
import type { ProxyReplacementTaskItem } from '@/services/api/usageService';
import type { CodexInspectionResultItem } from '@/features/monitoring/codexInspection';
import { reconcileProxyReplacementRechecks } from './proxyRecheckReconciliation';

const createResult = (
  key: string,
  proxySuspect: boolean
): CodexInspectionResultItem =>
  ({
    key,
    fileName: `${key}.json`,
    displayAccount: `${key}@example.com`,
    authIndex: null,
    accountId: null,
    provider: 'codex',
    disabled: false,
    autoRecoverOwned: false,
    status: '',
    state: '',
    raw: {},
    action: 'keep',
    actionReason: '',
    statusCode: null,
    usedPercent: null,
    isQuota: false,
    autoRecoverEligible: false,
    error: '',
    proxySuspect,
  }) as CodexInspectionResultItem;

const createTaskItem = (
  clientKey: string,
  status: ProxyReplacementTaskItem['status']
): ProxyReplacementTaskItem =>
  ({
    id: 1,
    taskId: 1,
    itemOrder: 0,
    target: { clientKey, fileName: `${clientKey}.json`, provider: 'codex' },
    status,
  }) as ProxyReplacementTaskItem;

describe('reconcileProxyReplacementRechecks', () => {
  it('clears direct proxy-abnormal state for healthy and inconclusive exit-IP rechecks only', () => {
    const rows = [
      createResult('success', true),
      createResult('inconclusive', true),
      createResult('failed', true),
      createResult('already-normal', false),
    ];
    rows[0].proxyVerificationSource = 'exit_ip';
    rows[1].proxyVerificationSource = 'exit_ip';
    rows[2].proxyVerificationSource = 'exit_ip';

    const reconciled = reconcileProxyReplacementRechecks(rows, [
      createTaskItem('success', 'recheck_succeeded'),
      createTaskItem('inconclusive', 'recheck_inconclusive'),
      createTaskItem('failed', 'recheck_failed'),
      createTaskItem('already-normal', 'recheck_succeeded'),
    ]);

    expect(reconciled.map((row) => [row.key, row.proxySuspect])).toEqual([
      ['success', false],
      ['inconclusive', false],
      ['failed', true],
      ['already-normal', false],
    ]);
    expect(reconciled[0].proxyRecheckSucceeded).toBe(true);
    expect(reconciled[1].proxyRecheckSucceeded).toBeUndefined();
    expect(reconciled[0].proxyVerificationSource).toBeUndefined();
    expect(reconciled[1].proxyVerificationSource).toBeUndefined();
    expect(reconciled[2]).toBe(rows[2]);
  });
});
