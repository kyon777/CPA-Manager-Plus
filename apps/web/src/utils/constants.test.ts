import { describe, expect, it } from 'vitest';
import { REQUEST_TIMEOUT_MS } from './constants';

describe('management request timeout', () => {
  it('keeps a full credential inventory request alive for sixty seconds', () => {
    expect(REQUEST_TIMEOUT_MS).toBe(60_000);
  });
});
