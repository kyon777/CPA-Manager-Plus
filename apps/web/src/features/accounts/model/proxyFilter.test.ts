import { describe, expect, it } from 'vitest';
import {
  findMissingEnabledProxyURLs,
  normalizeProxyFilterURL,
  normalizeProxyFilterURLs,
} from './proxyFilter';

describe('proxy filter model', () => {
  it('normalizes whitespace and trailing slashes', () => {
    expect(normalizeProxyFilterURL('  http://proxy.example:8080///  ')).toBe(
      'http://proxy.example:8080'
    );
    expect(normalizeProxyFilterURL('   ')).toBe('');
  });

  it('deduplicates normalized input while preserving first-seen order', () => {
    expect(
      normalizeProxyFilterURLs(['http://one/', ' http://two ', 'http://one///', '', 'http://two/'])
    ).toEqual(['http://one', 'http://two']);
  });

  it('finds values absent from every enabled credential across the full inventory', () => {
    const missing = findMissingEnabledProxyURLs(
      ['http://used/', 'http://missing/', 'http://disabled-only/'],
      [
        { name: 'used.json', disabled: false, proxy_url: 'http://used' },
        { name: 'disabled.json', disabled: true, proxy_url: 'http://disabled-only' },
        { name: 'other.json', disabled: false, proxyUrl: 'http://other/' },
        { name: 'runtime.json', disabled: false, runtime_only: 'true', proxy_url: 'http://runtime-only' },
      ]
    );
    expect(missing).toEqual(['http://missing', 'http://disabled-only']);
  });
});
