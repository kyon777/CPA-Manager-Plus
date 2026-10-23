import { describe, expect, it } from 'vitest';
import { buildAuthJsonExportTarget, buildAuthJsonExportTargets } from './authJsonExportTargets';

describe('auth JSON export targets', () => {
  it('builds an identity-only export target without credentials or proxy data', () => {
    const target = buildAuthJsonExportTarget({
      id: 'runtime-8',
      name: 'alice.json',
      authIndex: '8',
      provider: 'codex',
      email: 'Alice@Example.com',
      account_id: 'account-1',
      access_token: 'must-not-cross-browser-boundary',
      refresh_token: 'must-not-cross-browser-boundary',
      proxy_url: 'http://must-not-cross-browser-boundary',
    });

    expect(target).toEqual({
      clientKey: expect.stringContaining('alice.json'),
      fileName: 'alice.json',
      authIndex: '8',
      accountEmail: 'alice@example.com',
      provider: 'codex',
      runtimeId: 'runtime-8',
      accountId: 'account-1',
      accountSnapshot: 'alice@example.com',
    });
    expect(JSON.stringify(target)).not.toContain('must-not-cross-browser-boundary');
  });

  it('omits runtime-only entries and deduplicates repeated logical selections', () => {
    const selected = {
      id: 'runtime-8',
      name: 'alice.json',
      authIndex: '8',
      provider: 'codex',
      email: 'alice@example.com',
    };
    expect(buildAuthJsonExportTarget({ ...selected, runtimeOnly: true })).toBeNull();
    expect(buildAuthJsonExportTargets([selected, { ...selected }])).toHaveLength(1);
  });
});
