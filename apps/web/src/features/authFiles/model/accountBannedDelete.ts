import type {
  AccountBannedAuthFileDeleteTargetRequest,
  TokenRecoveryTask,
} from '@/services/api';

export type AccountBannedDeleteSource = {
  clientKey: string;
  fileName: string;
  authIndex?: string | number | null;
  accountEmail?: string | null;
  provider?: string | null;
  runtimeId?: string | null;
  accountId?: string | null;
  accountSnapshot?: string | null;
  [key: string]: unknown;
};

const terminalFailureStatuses = new Set([
  'auto_failed_manual_only',
  'manual_failed_manual_only',
]);

const stringValue = (value: unknown): string => (typeof value === 'string' ? value.trim() : '');

const normalizedProvider = (value: unknown): string => stringValue(value).toLowerCase().replace(/_/g, '-');
const normalizedEmail = (value: unknown): string => stringValue(value).toLowerCase();

// Keep this exactly aligned with Manager Server tokenrecovery.IsTerminalAccountBanned:
// account_banned is actionable only after a terminal recovery task, never while
// an automatic/manual recovery is still queued or running.
export const isTerminalAccountBannedRecoveryTask = (
  task: TokenRecoveryTask | null | undefined
): boolean => {
  if (!task || !terminalFailureStatuses.has(task.status)) return false;
  if ((task.lastErrorCode ?? '').trim().toLowerCase() === 'account_banned') return true;
  return /^account_banned(?:\s*:|\s*$)/i.test((task.lastErrorMessage ?? '').trim());
};

export const buildAccountBannedDeleteTarget = (
  source: AccountBannedDeleteSource,
  task: TokenRecoveryTask | null | undefined
): AccountBannedAuthFileDeleteTargetRequest | null => {
  if (!isTerminalAccountBannedRecoveryTask(task)) return null;
  const clientKey = stringValue(source.clientKey);
  const fileName = stringValue(source.fileName);
  const provider = normalizedProvider(source.provider);
  const authIndex = stringValue(source.authIndex);
  const runtimeId = stringValue(source.runtimeId);
  const accountEmail = normalizedEmail(source.accountEmail);
  const taskEmail = normalizedEmail(task?.accountEmail);
  const taskAuthIndex = stringValue(task?.authIndex);
  if (
    !clientKey ||
    !fileName ||
    provider !== 'codex' ||
    !accountEmail ||
    (!authIndex && !runtimeId) ||
    taskEmail !== accountEmail ||
    taskAuthIndex !== authIndex
  ) {
    return null;
  }
  const accountId = stringValue(source.accountId);
  const accountSnapshot = stringValue(source.accountSnapshot);
  return {
    clientKey,
    fileName,
    ...(authIndex ? { authIndex } : {}),
    ...(accountEmail ? { accountEmail } : {}),
    provider: 'codex',
    ...(runtimeId ? { runtimeId } : {}),
    ...(accountId ? { accountId } : {}),
    ...(accountSnapshot ? { accountSnapshot } : {}),
  };
};
