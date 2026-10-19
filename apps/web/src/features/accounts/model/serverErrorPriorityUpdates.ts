import type { ServerErrorPriorityDemotionUpdate } from '@/services/api';
import type { AuthFileItem } from '@/types';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { readAuthFileStatusAuthIndex, readAuthFileStatusPhysicalName, readAuthFileStatusProvider } from '@/utils/authFileStatusMutation';

export type ServerErrorPriorityUpdateMergeResult = {
  files: AuthFileItem[];
  changed: boolean;
  appliedCount: number;
};

const normalizedText = (value: unknown): string => String(value ?? '').trim().toLowerCase();

const sameCredential = (file: AuthFileItem, update: ServerErrorPriorityDemotionUpdate): boolean => {
  const fileName = readAuthFileStatusPhysicalName(file);
  const fileAuthIndex = readAuthFileStatusAuthIndex(file);
  return (
    fileName === String(update.fileName ?? '').trim() &&
    fileAuthIndex === normalizeAuthIndex(update.authIndex) &&
    readAuthFileStatusProvider(file) === normalizedText(update.provider)
  );
};

const readPriority = (file: AuthFileItem): number | null => {
  const raw = file.priority ?? file['priority'];
  const parsed = typeof raw === 'number' ? raw : Number(raw);
  return Number.isFinite(parsed) ? parsed : null;
};

// applyServerErrorPriorityUpdates is deliberately an in-memory-only merge.
// The `priorityBefore` comparison means a delayed queue notification can never
// overwrite a priority the operator changed after the server observed it.
export const applyServerErrorPriorityUpdates = (
  files: readonly AuthFileItem[],
  updates: readonly ServerErrorPriorityDemotionUpdate[]
): ServerErrorPriorityUpdateMergeResult => {
  if (files.length === 0 || updates.length === 0) {
    return { files: files as AuthFileItem[], changed: false, appliedCount: 0 };
  }

  let nextFiles: AuthFileItem[] | null = null;
  let appliedCount = 0;

  for (const update of updates) {
    if (
      !Number.isFinite(update.priorityBefore) ||
      !Number.isFinite(update.priorityTarget) ||
      !String(update.fileName ?? '').trim() ||
      !normalizeAuthIndex(update.authIndex) ||
      !normalizedText(update.provider)
    ) {
      continue;
    }
    const currentFiles = nextFiles ?? files;
    const index = currentFiles.findIndex(
      (file) => sameCredential(file, update) && readPriority(file) === update.priorityBefore
    );
    if (index < 0) continue;

    if (!nextFiles) nextFiles = [...files];
    const current = nextFiles[index];
    if (!current) continue;
    nextFiles[index] = { ...current, priority: update.priorityTarget };
    appliedCount += 1;
  }

  return {
    files: nextFiles ?? (files as AuthFileItem[]),
    changed: nextFiles !== null,
    appliedCount,
  };
};
