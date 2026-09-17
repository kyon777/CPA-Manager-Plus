import type { CodexInspectionResult } from '@/services/api/usageService';
import type { AuthFileItem } from '@/types';
import {
  readAuthFileStatusAccountIdInvalid,
  readAuthFileStatusCodexMemberInvalid,
  resolveCredentialIdentity,
} from '@/utils/authFileCredentialIdentity';
import { resolveAuthFileStatusMutationTarget } from '@/utils/authFileStatusMutation';

export type InspectionAccountNoteTarget = {
  fileName: string;
  runtimeId?: string | null;
  authIndex?: string | number | null;
  provider?: string;
  accountId?: string | null;
  accountSnapshot?: string | null;
};

const readTrimmedAccountNote = (file: AuthFileItem): string =>
  typeof file.note === 'string' ? file.note.trim() : '';

const resolveHistoricalResultIdentity = (result: InspectionAccountNoteTarget) =>
  resolveCredentialIdentity({
    name: result.fileName,
    runtimeId: result.runtimeId,
    authIndex: result.authIndex,
    provider: result.provider,
    accountId: result.accountId,
    accountSnapshot: result.accountSnapshot,
  });

const resolveReadOnlyHistoricalTarget = (
  result: InspectionAccountNoteTarget,
  files: AuthFileItem[]
): AuthFileItem | null => {
  const target = resolveHistoricalResultIdentity(result);
  if (!target.physicalName || !target.provider) return null;

  const hasCredentialLocator = Boolean(target.runtimeId || target.authIndex);
  const hasSafeFallbackIdentity =
    target.provider === 'codex'
      ? Boolean(target.accountSnapshot)
      : Boolean(target.accountId || target.accountSnapshot);
  if (!hasCredentialLocator && !hasSafeFallbackIdentity) return null;

  const matches = files.filter((file) => {
    if (
      target.provider === 'codex' &&
      (readAuthFileStatusAccountIdInvalid(file) || readAuthFileStatusCodexMemberInvalid(file))
    ) {
      return false;
    }
    const current = resolveCredentialIdentity(file);
    if (
      current.physicalName !== target.physicalName ||
      current.provider !== target.provider ||
      (target.runtimeId && current.runtimeId !== target.runtimeId)
    ) {
      return false;
    }
    if (target.authIndex) {
      if (current.authIndex !== target.authIndex) return false;
    } else if (target.runtimeId) {
      // The exact runtime ID was already checked above.
    } else if (target.provider === 'codex') {
      if (!target.accountSnapshot || current.accountSnapshot !== target.accountSnapshot)
        return false;
    } else {
      if (target.accountId && current.accountId !== target.accountId) return false;
      if (target.accountSnapshot && current.accountSnapshot !== target.accountSnapshot)
        return false;
    }
    if (target.accountId && current.accountId && current.accountId !== target.accountId)
      return false;
    if (
      target.accountSnapshot &&
      current.accountSnapshot &&
      current.accountSnapshot !== target.accountSnapshot
    ) {
      return false;
    }
    return true;
  });
  return matches.length === 1 ? matches[0] : null;
};

// Inspection results are historical records. Resolve their stable credential identity
// against the current auth-files response rather than matching only by display account.
export const resolveInspectionAccountNote = (
  result: InspectionAccountNoteTarget,
  files: AuthFileItem[]
): string => {
  const resolution = resolveAuthFileStatusMutationTarget(files, {
    name: result.fileName,
    runtimeId: result.runtimeId,
    authIndex: result.authIndex,
    provider: result.provider,
    accountId: result.accountId,
    accountSnapshot: result.accountSnapshot,
  });
  if (resolution.failure === null && resolution.target) {
    return readTrimmedAccountNote(resolution.target);
  }
  const historicalTarget = resolveReadOnlyHistoricalTarget(result, files);
  return historicalTarget ? readTrimmedAccountNote(historicalTarget) : '';
};

export const resolveServerInspectionAccountNote = (
  result: CodexInspectionResult,
  files: AuthFileItem[]
): string => resolveInspectionAccountNote(result, files);
