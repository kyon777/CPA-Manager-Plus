import { describe, expect, it } from 'vitest';
import type { AuthFileItem } from '@/types';
import type { ServerErrorPriorityDemotionUpdate } from '@/services/api';
import { applyServerErrorPriorityUpdates } from './serverErrorPriorityUpdates';

const update = (overrides: Partial<ServerErrorPriorityDemotionUpdate> = {}) => ({
  id: 42,
  fileName: 'account.json',
  authIndex: '7',
  provider: 'codex',
  priorityBefore: 27004,
  priorityTarget: 27003,
  completedAtMs: 1001,
  ...overrides,
});

const file = (overrides: Partial<AuthFileItem> = {}): AuthFileItem => ({
  name: 'account.json',
  authIndex: '7',
  provider: 'codex',
  priority: 27004,
  ...overrides,
});

describe('applyServerErrorPriorityUpdates', () => {
  it('updates the exact current credential after a completed demotion', () => {
    const files = [file(), file({ name: 'other.json', authIndex: '8', priority: 30000 })];

    const result = applyServerErrorPriorityUpdates(files, [update()]);

    expect(result.changed).toBe(true);
    expect(result.appliedCount).toBe(1);
    expect(result.files).toEqual([
      { ...files[0], priority: 27003 },
      files[1],
    ]);
  });

  it('does not overwrite a newer manual priority change', () => {
    const files = [file({ priority: 27100 })];

    const result = applyServerErrorPriorityUpdates(files, [update()]);

    expect(result.changed).toBe(false);
    expect(result.appliedCount).toBe(0);
    expect(result.files).toBe(files);
  });

  it('does not merge a mismatched provider or an unknown credential', () => {
    const files = [file()];

    const result = applyServerErrorPriorityUpdates(files, [
      update({ provider: 'xai' }),
      update({ id: 43, fileName: 'missing.json', authIndex: '8' }),
    ]);

    expect(result.changed).toBe(false);
    expect(result.files).toBe(files);
  });

  it('applies sequential updates for the same credential in cursor order', () => {
    const files = [file()];

    const result = applyServerErrorPriorityUpdates(files, [
      update({ priorityBefore: 27004, priorityTarget: 27003 }),
      update({ id: 43, priorityBefore: 27003, priorityTarget: 27002, completedAtMs: 1002 }),
    ]);

    expect(result.files[0]?.priority).toBe(27002);
    expect(result.appliedCount).toBe(2);
  });
});