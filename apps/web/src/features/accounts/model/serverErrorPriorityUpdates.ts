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

const credentialKey = (
  fileName: unknown,
  authIndex: unknown,
  provider: unknown
): string | null => {
  const normalizedFileName = String(fileName ?? '').trim();
  const normalizedAuthIndex = normalizeAuthIndex(authIndex);
  const normalizedProvider = normalizedText(provider);
  if (!normalizedFileName || !normalizedAuthIndex || !normalizedProvider) return null;
  return [normalizedFileName, normalizedAuthIndex, normalizedProvider].join('\u0000');
};

const credentialKeyForFile = (file: AuthFileItem): string | null =>
  credentialKey(
    readAuthFileStatusPhysicalName(file),
    readAuthFileStatusAuthIndex(file),
    readAuthFileStatusProvider(file)
  );

const credentialKeyForUpdate = (update: ServerErrorPriorityDemotionUpdate): string | null =>
  credentialKey(update.fileName, update.authIndex, update.provider);

const credentialIndexes = (files: readonly AuthFileItem[]): Map<string, number[]> => {
  const indexes = new Map<string, number[]>();
  files.forEach((file, index) => {
    const key = credentialKeyForFile(file);
    if (!key) return;
    const existing = indexes.get(key);
    if (existing) {
      existing.push(index);
      return;
    }
    indexes.set(key, [index]);
  });
  return indexes;
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
  const indexes = credentialIndexes(files);

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
    const matchingIndexes = indexes.get(credentialKeyForUpdate(update) ?? '');
    if (!matchingIndexes) continue;
    const currentFiles: readonly AuthFileItem[] = nextFiles ?? files;
    const index = matchingIndexes.find(
      (candidateIndex) => readPriority(currentFiles[candidateIndex]!) === update.priorityBefore
    );
    if (index === undefined) continue;

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
