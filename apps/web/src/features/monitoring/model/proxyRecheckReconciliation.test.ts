import { describe, expect, it } from 'vitest';
import type { ProxyReplacementTaskItem } from '@/services/api/usageService';
import type { CodexInspectionResultItem } from '@/features/monitoring/codexInspection';
import { reconcileSuccessfulProxyRechecks } from './proxyRecheckReconciliation';

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

describe('reconcileSuccessfulProxyRechecks', () => {
  it('removes only successful rechecks from the proxy-abnormal state', () => {
    const rows = [
      createResult('success', true),
      createResult('failed', true),
      createResult('already-normal', false),
    ];

    const reconciled = reconcileSuccessfulProxyRechecks(rows, [
      createTaskItem('success', 'recheck_succeeded'),
      createTaskItem('failed', 'recheck_failed'),
      createTaskItem('already-normal', 'recheck_succeeded'),
    ]);

    expect(reconciled.map((row) => [row.key, row.proxySuspect])).toEqual([
      ['success', false],
      ['failed', true],
      ['already-normal', false],
    ]);
    expect(reconciled[0].proxyRecheckSucceeded).toBe(true);
    expect(reconciled[2].proxyRecheckSucceeded).toBe(true);
    expect(reconciled[1]).toBe(rows[1]);
  });
});
