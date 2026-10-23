import type { AuthFileItem } from '@/types';
import {
  getAuthFilePatchTarget,
  getAuthFileSelectionKey,
} from '@/features/authFiles/model/credentialStatus';
import type { AuthJsonExportTargetRequest } from '@/services/api/usageService';

const normalizeProvider = (value: unknown): string =>
  typeof value === 'string' ? value.trim().toLowerCase() : '';

const normalizeEmail = (value: unknown): string => {
  const normalized = typeof value === 'string' ? value.trim().toLowerCase() : '';
  return normalized.includes('@') ? normalized : '';
};

const firstEmail = (...values: unknown[]): string => {
  for (const value of values) {
    const email = normalizeEmail(value);
    if (email) return email;
  }
  return '';
};

const isRuntimeOnly = (file: AuthFileItem): boolean => {
  const value = file.runtimeOnly ?? file['runtime_only'];
  return value === true || (typeof value === 'string' && value.trim().toLowerCase() === 'true');
};

// buildAuthJsonExportTarget is intentionally a one-way projection. The
// original AuthFileItem may contain raw credential fields, but none of them
// can cross from browser to Manager Server through this return value.
export const buildAuthJsonExportTarget = (
  file: AuthFileItem
): AuthJsonExportTargetRequest | null => {
  if (isRuntimeOnly(file)) return null;

  const patchTarget = getAuthFilePatchTarget(file);
  const clientKey = getAuthFileSelectionKey(file).trim();
  const fileName = String(patchTarget.name ?? '').trim();
  const provider = normalizeProvider(patchTarget.provider ?? file.provider ?? file.type);
  const authIndex =
    patchTarget.authIndex === undefined || patchTarget.authIndex === null
      ? ''
      : String(patchTarget.authIndex).trim();
  const runtimeId = String(patchTarget.runtimeId ?? '').trim();
  if (!clientKey || !fileName || !provider || (!authIndex && !runtimeId)) return null;

  const accountSnapshot = String(patchTarget.accountSnapshot ?? '').trim();
  const nestedUser = file.user as { email?: unknown } | undefined;
  const accountEmail = firstEmail(
    accountSnapshot,
    file.email,
    file.account,
    nestedUser?.email,
    file['account_email']
  );
  const accountId = String(patchTarget.accountId ?? '').trim();
  return {
    clientKey,
    fileName,
    provider,
    ...(authIndex ? { authIndex } : {}),
    ...(accountEmail ? { accountEmail } : {}),
    ...(runtimeId ? { runtimeId } : {}),
    ...(accountId ? { accountId } : {}),
    ...(accountSnapshot ? { accountSnapshot } : {}),
  };
};

export const buildAuthJsonExportTargets = (
  files: readonly AuthFileItem[]
): AuthJsonExportTargetRequest[] => {
  const seen = new Set<string>();
  const targets: AuthJsonExportTargetRequest[] = [];
  files.forEach((file) => {
    const target = buildAuthJsonExportTarget(file);
    if (!target || seen.has(target.clientKey)) return;
    seen.add(target.clientKey);
    targets.push(target);
  });
  return targets;
};
