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
import { buildAccountBannedDeleteTarget } from '@/features/authFiles/model/accountBannedDelete';
import { getAuthFilePatchTarget } from '@/features/authFiles/model/credentialStatus';
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

const readOptionalString = (record: Record<string, unknown>, key: string): string => {
  const value = record[key];
  return typeof value === 'string' ? value.trim() : '';
};

const readOptionalTimestamp = (record: Record<string, unknown>, key: string): number | null => {
  const value = record[key];
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
};

// The Manager projection explicitly strips any same-named field from the
// credential JSON before attaching this redacted SQLite task.  Parse only the
// small public task shape here too, so a list row can never turn arbitrary
// credential fields into runtime metadata.
const readEmbeddedRecoveryTask = (value: unknown): TokenRecoveryTask | null => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  const id = record.id;
  const fileName = readOptionalString(record, 'fileName');
  const provider = readOptionalString(record, 'provider');
  const status = readOptionalString(record, 'status');
  const mode = readOptionalString(record, 'mode');
  if (
    typeof id !== 'number' ||
    !Number.isSafeInteger(id) ||
    id <= 0 ||
    !fileName ||
    !provider ||
    !status ||
    !mode
  ) {
    return null;
  }
  const authIndex = readOptionalString(record, 'authIndex');
  const accountEmail = readOptionalString(record, 'accountEmail');
  const lastErrorCode = readOptionalString(record, 'lastErrorCode');
  const lastErrorMessage = readOptionalString(record, 'lastErrorMessage');
  const autoAttemptedAtMs = readOptionalTimestamp(record, 'autoAttemptedAtMs');
  const lastSignalAtMs = readOptionalTimestamp(record, 'lastSignalAtMs');
  const startedAtMs = readOptionalTimestamp(record, 'startedAtMs');
  const completedAtMs = readOptionalTimestamp(record, 'completedAtMs');
  const createdAtMs = readOptionalTimestamp(record, 'createdAtMs');
  const updatedAtMs = readOptionalTimestamp(record, 'updatedAtMs');
  return {
    id,
    fileName,
    provider,
    status,
    mode,
    ...(authIndex ? { authIndex } : {}),
    ...(accountEmail ? { accountEmail } : {}),
    ...(lastErrorCode ? { lastErrorCode } : {}),
    ...(lastErrorMessage ? { lastErrorMessage } : {}),
    ...(autoAttemptedAtMs === null ? {} : { autoAttemptedAtMs }),
    ...(lastSignalAtMs === null ? {} : { lastSignalAtMs }),
    ...(startedAtMs === null ? {} : { startedAtMs }),
    ...(completedAtMs === null ? {} : { completedAtMs }),
    ...(createdAtMs === null ? {} : { createdAtMs }),
    ...(updatedAtMs === null ? {} : { updatedAtMs }),
  };
};

export const buildEmbeddedCredentialRuntimeMetadataItems = (
  rows: readonly AccountRow[]
): CredentialRuntimeMetadataItem[] =>
  rows.flatMap((row) => {
    const clientKey = row.selectionKey.trim();
    if (!clientKey || normalizeProvider(row.provider) !== 'codex') return [];
    const raw = row.raw as Record<string, unknown>;
    const task = readEmbeddedRecoveryTask(raw.recovery_task);
    if (!task || normalizeProvider(task.provider) !== 'codex') return [];
    return [{ clientKey, recoveryTask: task }];
  });

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
  rows.filter((row) =>
    Boolean(
      buildPageAccountBannedDeleteTarget(
        row,
        metadataByClientKey.get(row.selectionKey)?.recoveryTask
      )
    )
  );

export const buildPageAccountBannedDeleteTarget = (
  row: AccountRow,
  task: TokenRecoveryTask | null | undefined
) => {
  if (row.runtimeOnly || normalizeProvider(row.provider) !== 'codex') return null;
  const patchTarget = getAuthFilePatchTarget(row.raw);
  return buildAccountBannedDeleteTarget(
    {
      clientKey: row.selectionKey,
      fileName: row.fileName || patchTarget.name,
      authIndex: patchTarget.authIndex ?? row.authIndex,
      accountEmail: readRowAccountEmail(row),
      provider: row.provider,
      runtimeId: patchTarget.runtimeId,
      accountId: patchTarget.accountId,
      accountSnapshot: patchTarget.accountSnapshot,
    },
    task
  );
};

export const buildPageAccountBannedDeleteTargets = (
  rows: readonly AccountRow[],
  metadataByClientKey: ReadonlyMap<string, CredentialRuntimeMetadataItem>
) =>
  rows.flatMap((row) => {
    const target = buildPageAccountBannedDeleteTarget(
      row,
      metadataByClientKey.get(row.selectionKey)?.recoveryTask
    );
    return target ? [target] : [];
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

export const automaticRecoveryCooldownMs = 24 * 60 * 60 * 1000;

export const hasAutomaticRecoveryAttempt = (task: TokenRecoveryTask | null | undefined): boolean =>
  Boolean(task && (task.autoAttemptedAtMs ?? 0) > 0);

export const isAutomaticRecoveryCoolingDown = (
  task: TokenRecoveryTask | null | undefined,
  nowMs = Date.now()
): boolean => {
  const attemptedAtMs = task?.autoAttemptedAtMs ?? 0;
  return (
    attemptedAtMs > 0 &&
    (nowMs <= attemptedAtMs || nowMs - attemptedAtMs < automaticRecoveryCooldownMs)
  );
};

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
