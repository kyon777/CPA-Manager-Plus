import { describe, expect, it } from 'vitest';
import type { CodexInspectionResult } from '@/services/api/usageService';
import type { AuthFileItem } from '@/types';
import { resolveServerInspectionAccountNote } from './serverInspectionAccountNote';

const result = (overrides: Partial<CodexInspectionResult> = {}): CodexInspectionResult => ({
  id: 1,
  runId: 7,
  accountKey: 'shared.json::auth-b',
  fileName: 'shared.json',
  displayAccount: 'bob@example.com',
  authIndex: 'auth-b',
  accountId: 'workspace-a',
  accountSnapshot: 'bob@example.com',
  provider: 'codex',
  disabled: false,
  action: 'keep',
  actionReason: '',
  isQuota: false,
  createdAtMs: 0,
  ...overrides,
});

const file = (overrides: Partial<AuthFileItem> = {}): AuthFileItem => ({
  id: 'runtime-auth-b',
  name: 'shared.json',
  provider: 'codex',
  authIndex: 'auth-b',
  account: 'bob@example.com',
  account_id: 'workspace-a',
  note: '  Codex B Pool  ',
  ...overrides,
});

describe('server inspection account note resolver', () => {
  it('uses the current matching JSON credential note and trims display whitespace', () => {
    const note = resolveServerInspectionAccountNote(result(), [
      file({
        id: 'runtime-auth-a',
        authIndex: 'auth-a',
        account: 'alice@example.com',
        note: 'Codex A Pool',
      }),
      file(),
    ]);

    expect(note).toBe('Codex B Pool');
  });

  it('uses a unique Codex member snapshot when an older result has no auth index', () => {
    const note = resolveServerInspectionAccountNote(result({ authIndex: undefined }), [
      file({
        id: 'runtime-auth-a',
        authIndex: undefined,
        account: 'alice@example.com',
        note: 'Codex A Pool',
      }),
      file({ authIndex: undefined }),
    ]);

    expect(note).toBe('Codex B Pool');
  });

  it('omits a note when the snapshot match is ambiguous', () => {
    const note = resolveServerInspectionAccountNote(result({ authIndex: undefined }), [
      file({ authIndex: undefined }),
      file({ id: 'runtime-auth-b-copy', authIndex: undefined }),
    ]);

    expect(note).toBe('');
  });

  it('does not join a Codex member from a different workspace during fallback matching', () => {
    const note = resolveServerInspectionAccountNote(result({ authIndex: undefined }), [
      file({ authIndex: undefined, account_id: 'workspace-b' }),
    ]);

    expect(note).toBe('');
  });

  it('does not leak a note when the historical result cannot identify one current credential', () => {
    const note = resolveServerInspectionAccountNote(
      result({ authIndex: undefined, accountId: undefined, accountSnapshot: undefined }),
      [file()]
    );

    expect(note).toBe('');
  });

  it('does not show a blank JSON note', () => {
    expect(resolveServerInspectionAccountNote(result(), [file({ note: '  ' })])).toBe('');
  });
});
