import { describe, expect, it } from 'vitest';
import { LOGS_TIMEOUT_MS, REQUEST_TIMEOUT_MS } from './constants';

describe('management request timeout', () => {
  it('keeps direct CPA inventory and log requests alive for four minutes', () => {
    expect(REQUEST_TIMEOUT_MS).toBe(240_000);
    expect(LOGS_TIMEOUT_MS).toBe(240_000);
  });
});
