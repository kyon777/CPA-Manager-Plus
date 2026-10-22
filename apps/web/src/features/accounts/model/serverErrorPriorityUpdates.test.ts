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

  it('keeps the first matching priority semantics for duplicate credential identities', () => {
    const files = [file({ priority: 27004 }), file({ priority: 27004 })];

    const result = applyServerErrorPriorityUpdates(files, [
      update({ priorityBefore: 27004, priorityTarget: 27003 }),
      update({ id: 43, priorityBefore: 27004, priorityTarget: 27002, completedAtMs: 1002 }),
    ]);

    expect(result.files.map((entry) => entry.priority)).toEqual([27003, 27002]);
    expect(result.appliedCount).toBe(2);
  });

  it('builds one credential lookup for a large update batch instead of scanning findIndex per delta', () => {
    const files = Array.from({ length: 4_000 }, (_, index) =>
      file({
        name: `credential-${index}.json`,
        authIndex: String(index),
        priority: 30_000 + index,
      })
    );
    const updates = Array.from({ length: 200 }, (_, index) =>
      update({
        id: index + 1,
        fileName: `credential-${index * 10}.json`,
        authIndex: String(index * 10),
        priorityBefore: 30_000 + index * 10,
        priorityTarget: 29_999 + index * 10,
      })
    );
    const withoutFindIndex = new Proxy(files, {
      get(target, property, receiver) {
        if (property === 'findIndex') {
          throw new Error('priority merge must use its batch identity index');
        }
        return Reflect.get(target, property, receiver);
      },
    });

    const result = applyServerErrorPriorityUpdates(withoutFindIndex, updates);

    expect(result.appliedCount).toBe(200);
    expect(result.files[0]?.priority).toBe(29_999);
    expect(result.files[1]?.priority).toBe(files[1]?.priority);
    expect(result.files[1]).toBe(files[1]);
    expect(result.files[1_990]?.priority).toBe(31_989);
  });
});
