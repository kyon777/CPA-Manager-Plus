import type { AuthFileItem } from '@/types';
import {
  getAuthFilePatchTarget,
  getAuthFileSelectionKey,
} from '@/features/authFiles/model/credentialStatus';
import type { ProxyReplacementTarget } from './proxyReplacement';

const normalizeProvider = (value: unknown) =>
  typeof value === 'string' ? value.trim().toLowerCase() : '';

const normalizeEmail = (value: unknown) => {
  const normalized = typeof value === 'string' ? value.trim().toLowerCase() : '';
  return normalized.includes('@') ? normalized : '';
};

const firstEmail = (...values: unknown[]) => {
  for (const value of values) {
    const email = normalizeEmail(value);
    if (email) return email;
  }
  return '';
};

// This is deliberately identity-only. It does not carry raw auth JSON,
// tokens, proxy values, quota data, or arbitrary fields to Manager Server.
export const buildProxyReplacementTarget = (
  file: AuthFileItem
): ProxyReplacementTarget | null => {
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

export const buildProxyReplacementTargets = (
  files: readonly AuthFileItem[]
): ProxyReplacementTarget[] => {
  const seen = new Set<string>();
  const targets: ProxyReplacementTarget[] = [];
  files.forEach((file) => {
    const target = buildProxyReplacementTarget(file);
    if (!target || seen.has(target.clientKey)) return;
    seen.add(target.clientKey);
    targets.push(target);
  });
  return targets;
};

// Inspection rows already contain the stable identity projected by CPA/Manager.
// Keep the browser request identity-only; raw credential payloads and proxy
// values never cross this boundary.
export type ProxyReplacementInspectionIdentity = {
  key: string;
  fileName: string;
  provider: string;
  authIndex?: string | null;
  runtimeId?: string | null;
  accountId?: string | null;
  accountSnapshot?: string | null;
  displayAccount?: string | null;
};

export const buildInspectionProxyReplacementTargets = (
  items: readonly ProxyReplacementInspectionIdentity[]
): ProxyReplacementTarget[] => {
  const seen = new Set<string>();
  const targets: ProxyReplacementTarget[] = [];
  items.forEach((item) => {
    const clientKey = String(item.key ?? '').trim();
    const fileName = String(item.fileName ?? '').trim();
    const provider = normalizeProvider(item.provider);
    const authIndex = String(item.authIndex ?? '').trim();
    const runtimeId = String(item.runtimeId ?? '').trim();
    // The directed post-write recheck is a Codex probe. Do not enqueue an xAI
    // row here and then claim it was rechecked by a different probe surface.
    if (
      !clientKey ||
      !fileName ||
      provider !== 'codex' ||
      (!authIndex && !runtimeId) ||
      seen.has(clientKey)
    ) {
      return;
    }
    seen.add(clientKey);
    const accountSnapshot = String(item.accountSnapshot ?? '').trim();
    const accountEmail = firstEmail(accountSnapshot, item.displayAccount);
    const accountId = String(item.accountId ?? '').trim();
    targets.push({
      clientKey,
      fileName,
      provider,
      ...(authIndex ? { authIndex } : {}),
      ...(accountEmail ? { accountEmail } : {}),
      ...(runtimeId ? { runtimeId } : {}),
      ...(accountId ? { accountId } : {}),
      ...(accountSnapshot ? { accountSnapshot } : {}),
    });
  });
  return targets;
};
