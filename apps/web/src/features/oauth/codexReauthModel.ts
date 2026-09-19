import type { AuthFileItem } from '@/types';
import type { TokenRecoveryTargetRequest } from '@/services/api';
import {
  readAuthFileStatusAccountId,
  readAuthFileStatusCodexMember,
  readAuthFileStatusProvider,
  readAuthFileStatusRuntimeId,
} from '@/utils/authFileStatusMutation';

export type CodexReauthReconciliationCode =
  | 'identity_changed'
  | 'identity_ambiguous'
  | 'identity_unconfirmed';

export class CodexReauthReconciliationError extends Error {
  readonly code: CodexReauthReconciliationCode;

  constructor(code: CodexReauthReconciliationCode, message: string) {
    super(message);
    this.code = code;
    Object.defineProperty(this, 'name', {
      value: 'CodexReauthReconciliationError',
      enumerable: false,
      configurable: true,
    });
  }
}

export const isCodexReauthReconciliationError = (
  error: unknown
): error is CodexReauthReconciliationError => error instanceof CodexReauthReconciliationError;

export type CodexReauthTarget = {
  account: string;
  fileName?: string;
  runtimeId?: string | null;
  provider?: string | null;
  authIndex?: string | number | null;
  accountId?: string | null;
  accountSnapshot?: string | null;
};

export type CodexTokenRecoveryTargetParts = {
  account?: string | null;
  accountSnapshot?: string | null;
  authIndex?: string | number | null;
  fileName?: string | null;
};

const readStringField = (source: Record<string, unknown>, keys: string[]): string => {
  for (const key of keys) {
    const value = source[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
    if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  }
  return '';
};

const normalizeRecoveryEmail = (value: unknown): string => {
  if (typeof value !== 'string') return '';
  const normalized = value.trim();
  return normalized.includes('@') ? normalized : '';
};

// buildCodexTokenRecoveryTarget centralizes the only browser-to-manager
// recovery locator. It deliberately carries no account ID, proxy, or token.
export const buildCodexTokenRecoveryTarget = (
  parts: CodexTokenRecoveryTargetParts
): TokenRecoveryTargetRequest | null => {
  const fileName = parts.fileName?.trim() || '';
  const authIndex =
    parts.authIndex === null || parts.authIndex === undefined ? '' : String(parts.authIndex).trim();
  const accountEmail =
    normalizeRecoveryEmail(parts.accountSnapshot) || normalizeRecoveryEmail(parts.account);
  if (!fileName || (!authIndex && !accountEmail)) return null;
  return {
    fileName,
    ...(authIndex ? { authIndex } : {}),
    ...(accountEmail ? { accountEmail } : {}),
    provider: 'codex',
  };
};

export const createCodexReauthTargetFromAuthFile = (file: AuthFileItem): CodexReauthTarget => {
  const record = file as Record<string, unknown>;
  const account =
    readStringField(record, [
      'email',
      'account',
      'displayAccount',
      'display_account',
      'accountEmail',
      'account_email',
      'user',
      'username',
    ]) || file.name;
  const accountId = readAuthFileStatusAccountId(file) || null;
  return {
    account,
    fileName: file.name,
    runtimeId: readAuthFileStatusRuntimeId(file) || null,
    provider: readAuthFileStatusProvider(file) || null,
    authIndex: (record.authIndex ?? record.auth_index ?? null) as string | number | null,
    accountId,
    accountSnapshot: readAuthFileStatusCodexMember(file) || null,
  };
};
