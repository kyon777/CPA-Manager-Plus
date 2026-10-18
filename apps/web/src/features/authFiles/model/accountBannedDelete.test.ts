import { describe, expect, it } from 'vitest';
import type { TokenRecoveryTask } from '@/services/api';
import {
  buildAccountBannedDeleteTarget,
  isTerminalAccountBannedRecoveryTask,
} from './accountBannedDelete';

const bannedTask = (overrides: Partial<TokenRecoveryTask> = {}): TokenRecoveryTask => ({
  id: 1,
  fileName: 'a.json',
  authIndex: '7',
  accountEmail: 'person@example.com',
  provider: 'codex',
  status: 'manual_failed_manual_only',
  mode: 'manual',
  lastErrorCode: 'account_banned',
  ...overrides,
});

describe('accountBannedDelete', () => {
  it('maps only terminal account_banned identities without raw credential fields', () => {
    const target = buildAccountBannedDeleteTarget(
      {
        clientKey: 'row-a',
        fileName: 'a.json',
        authIndex: '7',
        accountEmail: 'person@example.com',
        provider: 'codex',
        runtimeId: 'runtime-a',
        accountId: 'account-a',
        accountSnapshot: 'person@example.com',
        // Deliberately unknown to the source contract: mapper must not copy it.
        accessToken: 'must-not-leak',
        proxyUrl: 'http://user:password@proxy.example',
      },
      bannedTask()
    );

    expect(target).toEqual({
      clientKey: 'row-a',
      fileName: 'a.json',
      authIndex: '7',
      accountEmail: 'person@example.com',
      provider: 'codex',
      runtimeId: 'runtime-a',
      accountId: 'account-a',
      accountSnapshot: 'person@example.com',
    });
    expect(JSON.stringify(target)).not.toContain('must-not-leak');
    expect(JSON.stringify(target)).not.toContain('password');
  });

  it('rejects nonterminal, nonbanned, noncodex, and unresolvable sources', () => {
    const source = {
      clientKey: 'row-a',
      fileName: 'a.json',
      authIndex: '7',
      accountEmail: 'person@example.com',
      provider: 'codex',
      runtimeId: 'runtime-a',
    };
    expect(buildAccountBannedDeleteTarget(source, bannedTask({ status: 'manual_running' }))).toBeNull();
    expect(buildAccountBannedDeleteTarget(source, bannedTask({ lastErrorCode: 'http_401' }))).toBeNull();
    expect(buildAccountBannedDeleteTarget({ ...source, provider: 'xai' }, bannedTask())).toBeNull();
    expect(buildAccountBannedDeleteTarget({ ...source, authIndex: '', runtimeId: '' }, bannedTask())).toBeNull();
    expect(buildAccountBannedDeleteTarget({ ...source, accountEmail: '' }, bannedTask())).toBeNull();
    expect(
      buildAccountBannedDeleteTarget(
        source,
        bannedTask({ accountEmail: 'replacement@example.com' })
      )
    ).toBeNull();
    expect(
      buildAccountBannedDeleteTarget(source, bannedTask({ authIndex: 'replacement-index' }))
    ).toBeNull();
  });

  it('uses the bounded account_banned message fallback', () => {
    expect(
      isTerminalAccountBannedRecoveryTask(
        bannedTask({ lastErrorCode: '', lastErrorMessage: ' account_banned: disabled ' })
      )
    ).toBe(true);
    expect(
      isTerminalAccountBannedRecoveryTask(
        bannedTask({ lastErrorCode: '', lastErrorMessage: 'not_account_banned' })
      )
    ).toBe(false);
  });
});
