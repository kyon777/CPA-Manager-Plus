import { describe, expect, it } from 'vitest';
import { USAGE_SERVICE_TIMEOUT_MS } from './usageService';

describe('usage-service request timeout', () => {
  it('matches the sixty-second full-inventory budget', () => {
    expect(USAGE_SERVICE_TIMEOUT_MS).toBe(60_000);
  });
});
