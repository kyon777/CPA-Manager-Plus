import { describe, expect, it } from 'vitest';
import type { AccountRowSort } from './accountRows';
import { buildCredentialListRequest, isServerCredentialListEligible } from './credentialListQuery';

describe('credential list server query', () => {
  it('maps the visible workspace query to the bounded manager endpoint', () => {
    expect(
      buildCredentialListRequest({
        page: 3,
        pageSize: 50,
        search: ' Alice ',
        provider: 'codex',
        status: 'enabled',
        sort: { key: 'recent', direction: 'desc' },
      })
    ).toEqual({
      page: 3,
      page_size: 50,
      search: 'Alice',
      provider: 'codex',
      status: 'enabled',
      sort: 'recent_desc',
      include_proxy_url: 1,
    });
  });

  it('only enables server paging when client-only filters are inactive', () => {
    const base = {
      activeView: 'accounts' as const,
      managerStorageAvailable: true,
      credentialListAvailable: true,
      pageSize: 50,
      provider: 'all',
      status: 'all',
      plan: 'all',
      quotaBand: 'all' as const,
      operational: 'all' as const,
      sort: { key: 'recent', direction: 'desc' } as AccountRowSort,
    };
    expect(isServerCredentialListEligible(base)).toBe(true);
    expect(isServerCredentialListEligible({ ...base, plan: 'free' })).toBe(false);
    expect(isServerCredentialListEligible({ ...base, quotaBand: 'lt20' })).toBe(false);
    expect(isServerCredentialListEligible({ ...base, operational: 'reauth' })).toBe(false);
    expect(isServerCredentialListEligible({ ...base, status: 'problem' })).toBe(false);
    expect(isServerCredentialListEligible({ ...base, pageSize: 100 })).toBe(false);
  });
});
