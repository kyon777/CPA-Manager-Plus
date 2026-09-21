import { describe, expect, it } from 'vitest';
import type { AccountRow } from './accountRows';
import type { CredentialRuntimeMetadataItem, TokenRecoveryStatus } from '@/services/api/usageService';
import {
  buildCredentialRuntimeMetadataTargets,
  buildPageAccountBannedRows,
  buildPageRecoveryCandidates,
  formatRecoveryState,
  hasAutomaticRecoveryAttempt,
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
  it('selects only current-page Codex rows with terminal account_banned recovery errors', () => {
    const banned = makeRow({ selectionKey: 'row-banned', fileName: 'banned.json' });
    const caseVariant = makeRow({ selectionKey: 'row-case', fileName: 'case.json' });
    const pending = makeRow({ selectionKey: 'row-pending', fileName: 'pending.json' });
    const runtimeOnly = makeRow({
      selectionKey: 'row-runtime',
      fileName: 'runtime.json',
      runtimeOnly: true,
    });
    const nonCodex = makeRow({ selectionKey: 'row-xai', fileName: 'xai.json', provider: 'xai' });
    const metadata = (clientKey: string, status: TokenRecoveryStatus, lastErrorCode?: string) =>
      ({
        clientKey,
        recoveryTask: {
          id: 1,
          fileName: `${clientKey}.json`,
          provider: 'codex',
          status,
          mode: 'manual',
          ...(lastErrorCode === undefined ? {} : { lastErrorCode }),
        },
      }) satisfies CredentialRuntimeMetadataItem;

    const rows = buildPageAccountBannedRows(
      [banned, caseVariant, pending, runtimeOnly, nonCodex],
      new Map([
        ['row-banned', metadata('row-banned', 'manual_failed_manual_only', 'account_banned')],
        ['row-case', metadata('row-case', 'auto_failed_manual_only', ' ACCOUNT_BANNED ')],
        ['row-pending', metadata('row-pending', 'manual_running', 'account_banned')],
        ['row-runtime', metadata('row-runtime', 'manual_failed_manual_only', 'account_banned')],
        ['row-xai', metadata('row-xai', 'manual_failed_manual_only', 'account_banned')],
      ])
    );

    expect(rows.map((row) => row.selectionKey)).toEqual(['row-banned', 'row-case']);
  });

  it('ignores terminal recovery tasks without an exact banned error code', () => {
    const blank = makeRow({ selectionKey: 'row-blank', fileName: 'blank.json' });
    const missing = makeRow({ selectionKey: 'row-missing', fileName: 'missing.json' });
    const other = makeRow({ selectionKey: 'row-other', fileName: 'other.json' });
    const task = (clientKey: string, lastErrorCode?: string) =>
      ({
        clientKey,
        recoveryTask: {
          id: 2,
          fileName: `${clientKey}.json`,
          provider: 'codex',
          status: 'manual_failed_manual_only',
          mode: 'manual',
          ...(lastErrorCode === undefined ? {} : { lastErrorCode }),
        },
      }) satisfies CredentialRuntimeMetadataItem;

    expect(
      buildPageAccountBannedRows(
        [blank, missing, other],
        new Map([
          ['row-blank', task('row-blank', '   ')],
          ['row-missing', task('row-missing')],
          ['row-other', task('row-other', 'account_disabled')],
        ])
      )
    ).toEqual([]);
  });

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

  it('recognizes the durable automatic-attempt audit marker independently of current task state', () => {
    expect(hasAutomaticRecoveryAttempt({ autoAttemptedAtMs: 123 } as never)).toBe(true);
    expect(hasAutomaticRecoveryAttempt({ autoAttemptedAtMs: 0 } as never)).toBe(false);
    expect(hasAutomaticRecoveryAttempt(null)).toBe(false);
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
