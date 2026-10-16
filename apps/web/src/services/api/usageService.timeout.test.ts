import { describe, expect, it } from 'vitest';
import { USAGE_SERVICE_TIMEOUT_MS } from './usageService';

describe('usage-service request timeout', () => {
  it('matches the four-minute full-inventory budget', () => {
    expect(USAGE_SERVICE_TIMEOUT_MS).toBe(240_000);
  });
});
