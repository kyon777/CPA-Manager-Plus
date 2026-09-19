import type {
  CredentialRuntimeMetadataItem,
  CredentialRuntimeMetadataTarget,
  TokenRecoveryBatchTargetRequest,
  TokenRecoveryStatus,
  TokenRecoveryTask,
} from '@/services/api';
import {
  buildCodexTokenRecoveryTarget,
  createCodexReauthTargetFromAuthFile,
} from '@/features/oauth/codexReauthModel';
import type { AccountRow } from './accountRows';

export type RecoveryPresentation = {
  tone: 'info' | 'success' | 'danger';
  labelKey:
    | 'accounts.recovery_auto_processing'
    | 'accounts.recovery_manual_processing'
    | 'accounts.recovery_succeeded'
    | 'accounts.recovery_failed_reason'
    | 'accounts.recovery_failed_generic';
  values?: Record<string, string>;
};

const pendingRecoveryStatuses = new Set<TokenRecoveryStatus>([
  'auto_queued',
  'auto_running',
  'manual_queued',
  'manual_running',
]);

const failedRecoveryStatuses = new Set<TokenRecoveryStatus>([
  'auto_failed_manual_only',
  'manual_failed_manual_only',
]);

const normalizeProvider = (value: string | null | undefined): string => {
  const normalized = (value ?? '').trim().toLowerCase().replace(/_/g, '-');
  if (normalized === 'x-ai' || normalized === 'grok') return 'xai';
  return normalized || 'unknown';
};

const normalizeEmail = (value: unknown): string => {
  if (typeof value !== 'string') return '';
  const normalized = value.trim();
  return normalized.includes('@') ? normalized : '';
};

const readRowAccountEmail = (row: AccountRow): string => {
  const raw = row.raw as Record<string, unknown>;
  for (const key of ['email', 'account', 'account_email', 'accountEmail', 'username']) {
    const email = normalizeEmail(raw[key]);
    if (email) return email;
  }
  return normalizeEmail(row.accountLabel);
};

export const buildCredentialRuntimeMetadataTargets = (
  rows: readonly AccountRow[]
): CredentialRuntimeMetadataTarget[] =>
  rows.flatMap((row) => {
    const clientKey = row.selectionKey.trim();
    const fileName = row.fileName.trim();
    if (!clientKey || !fileName) return [];
    const authIndex = row.authIndex.trim();
    const accountEmail = readRowAccountEmail(row);
    return [
      {
        clientKey,
        fileName,
        ...(authIndex ? { authIndex } : {}),
        ...(accountEmail ? { accountEmail } : {}),
        provider: normalizeProvider(row.provider),
      },
    ];
  });

export const buildPageAccountBannedRows = (
  rows: readonly AccountRow[],
  metadataByClientKey: ReadonlyMap<string, CredentialRuntimeMetadataItem>
): AccountRow[] =>
  rows.filter((row) => {
    if (row.runtimeOnly || normalizeProvider(row.provider) !== 'codex') return false;
    const task = metadataByClientKey.get(row.selectionKey)?.recoveryTask;
    if (!task || !failedRecoveryStatuses.has(task.status)) return false;
    return (task.lastErrorCode ?? '').trim().toLowerCase() === 'account_banned';
  });

export const buildPageRecoveryCandidates = (
  rows: readonly AccountRow[],
  healthStatusBySelectionKey: ReadonlyMap<string, string | undefined>
): TokenRecoveryBatchTargetRequest[] =>
  rows.flatMap((row) => {
    if (
      row.runtimeOnly ||
      normalizeProvider(row.provider) !== 'codex' ||
      healthStatusBySelectionKey.get(row.selectionKey) !== 'reauth'
    ) {
      return [];
    }
    const reauthTarget = createCodexReauthTargetFromAuthFile(row.raw);
    const target = buildCodexTokenRecoveryTarget({
      account: reauthTarget.account || row.accountLabel,
      accountSnapshot: reauthTarget.accountSnapshot,
      authIndex: reauthTarget.authIndex ?? row.authIndex,
      fileName: row.fileName || reauthTarget.fileName,
    });
    const clientKey = row.selectionKey.trim();
    return target && clientKey ? [{ clientKey, ...target }] : [];
  });

export const isPendingRecoveryStatus = (status: TokenRecoveryStatus | null | undefined): boolean =>
  Boolean(status && pendingRecoveryStatuses.has(status));

export const formatRecoveryState = (
  task: TokenRecoveryTask | null | undefined
): RecoveryPresentation | null => {
  const status = task?.status;
  if (!status) return null;
  if (status === 'auto_queued' || status === 'auto_running') {
    return { tone: 'info', labelKey: 'accounts.recovery_auto_processing' };
  }
  if (status === 'manual_queued' || status === 'manual_running') {
    return { tone: 'info', labelKey: 'accounts.recovery_manual_processing' };
  }
  if (status === 'succeeded') {
    return { tone: 'success', labelKey: 'accounts.recovery_succeeded' };
  }
  if (failedRecoveryStatuses.has(status)) {
    const reason = task?.lastErrorMessage?.trim() || task?.lastErrorCode?.trim() || '';
    return reason
      ? { tone: 'danger', labelKey: 'accounts.recovery_failed_reason', values: { reason } }
      : { tone: 'danger', labelKey: 'accounts.recovery_failed_generic' };
  }
  return null;
};
