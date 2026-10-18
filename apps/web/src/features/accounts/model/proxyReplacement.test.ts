import { describe, expect, it } from 'vitest';
import {
  isLocalProxySuspect,
  normalizeProxyReplacementURL,
  proxyReplacementFingerprint,
} from './proxyReplacement';

describe('proxy replacement model', () => {
  it('normalizes whitespace and trailing slashes before fingerprinting', () => {
    expect(normalizeProxyReplacementURL('  http://proxy.example:8080///  ')).toBe(
      'http://proxy.example:8080'
    );
    expect(proxyReplacementFingerprint('http://proxy.example:8080/')).toBe(
      proxyReplacementFingerprint('http://proxy.example:8080')
    );
    expect(proxyReplacementFingerprint('http://u:p@host:8080')).toBe(
      '57273d36dacfbc6be8ed5db14b21bd26c2b890265bf5378edf7ddae6e1752e04'
    );
    expect(proxyReplacementFingerprint('')).toBe('');
  });

  it('only marks a proxied local inspection transport failure as suspected', () => {
    expect(
      isLocalProxySuspect({
        proxyUrl: 'http://p:1/',
        errorKind: 'request_error',
        errorDetail: 'api-call failed: 502 {"error":"request failed"}',
      })
    ).toBe(true);
    expect(
      isLocalProxySuspect({
        proxyUrl: 'http://p:1',
        statusCode: 429,
        errorKind: 'http_status',
        errorDetail: 'Rate limit exceeded',
      })
    ).toBe(false);
    expect(
      isLocalProxySuspect({
        proxyUrl: '',
        errorKind: 'request_error',
        errorDetail: 'context deadline exceeded',
      })
    ).toBe(false);
  });
});
