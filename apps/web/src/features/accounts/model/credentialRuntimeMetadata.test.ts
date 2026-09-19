import { describe, expect, it } from 'vitest';
import type { AccountRow } from './accountRows';
import {
  buildCredentialRuntimeMetadataTargets,
  buildPageRecoveryCandidates,
  formatRecoveryState,
  isPendingRecoveryStatus,
} from './credentialRuntimeMetadata';
import { buildCodexTokenRecoveryTarget } from '@/features/oauth/codexReauthModel';

const makeRow = (overrides: Partial<AccountRow> = {}): AccountRow =>
  ({
    key: 'a.json',
    selectionKey: 'row-a',
    fileName: 'a.json',
    accountLabel: 'person@example.com',
    provider: 'codex',
    planType: null,
    disabled: false,
    runtimeOnly: false,
    statusMessage: '',
    authIndex: '7',
    projectId: '',
    priority: null,
    createdAtMs: null,
    updatedAtMs: null,
    subscriptionUntilMs: null,
    authenticationAtMs: 0,
    rawCredentialStatusSuperseded: false,
    quota: {} as AccountRow['quota'],
    usage: {} as AccountRow['usage'],
    inspection: null,
    raw: {
      name: 'a.json',
      email: 'person@example.com',
      authIndex: '7',
      type: 'codex',
    } as AccountRow['raw'],
    ...overrides,
  }) as AccountRow;

describe('credential runtime metadata model', () => {
  it('creates redacted metadata locators from visible rows', () => {
    const targets = buildCredentialRuntimeMetadataTargets([
      makeRow(),
      makeRow({ selectionKey: '', fileName: '' }),
    ]);

    expect(targets).toEqual([
      {
        clientKey: 'row-a',
        fileName: 'a.json',
        authIndex: '7',
        accountEmail: 'person@example.com',
        provider: 'codex',
      },
    ]);
    expect(JSON.stringify(targets)).not.toContain('access_token');
  });

  it('selects only current-page Codex reauth rows with usable recovery locators', () => {
    const reauth = makeRow();
    const available = makeRow({ selectionKey: 'row-b', fileName: 'b.json' });
    const runtimeOnly = makeRow({ selectionKey: 'row-c', fileName: 'c.json', runtimeOnly: true });
    const nonCodex = makeRow({ selectionKey: 'row-d', fileName: 'd.json', provider: 'xai' });
    const candidates = buildPageRecoveryCandidates(
      [reauth, available, runtimeOnly, nonCodex],
      new Map([
        ['row-a', 'reauth'],
        ['row-b', 'available'],
        ['row-c', 'reauth'],
        ['row-d', 'reauth'],
      ])
    );

    expect(candidates).toEqual([
      {
        clientKey: 'row-a',
        fileName: 'a.json',
        authIndex: '7',
        accountEmail: 'person@example.com',
        provider: 'codex',
      },
    ]);
  });

  it('formats pending and failure states with the returned failure reason', () => {
    expect(isPendingRecoveryStatus('manual_running')).toBe(true);
    expect(isPendingRecoveryStatus('succeeded')).toBe(false);
    expect(formatRecoveryState({ status: 'auto_queued' } as never)).toEqual({
      tone: 'info',
      labelKey: 'accounts.recovery_auto_processing',
    });
    expect(
      formatRecoveryState({
        status: 'auto_failed_manual_only',
        lastErrorMessage: 'invalid_proxy',
      } as never)
    ).toEqual({
      tone: 'danger',
      labelKey: 'accounts.recovery_failed_reason',
      values: { reason: 'invalid_proxy' },
    });
  });

  it('uses the verified Codex member email when making a recovery locator', () => {
    expect(
      buildCodexTokenRecoveryTarget({
        account: 'display name',
        accountSnapshot: 'member@example.com',
        authIndex: 9,
        fileName: 'codex.json',
      })
    ).toEqual({
      fileName: 'codex.json',
      authIndex: '9',
      accountEmail: 'member@example.com',
      provider: 'codex',
    });
  });
});
