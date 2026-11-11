import {
  normalizeProxyReplacementURL,
  proxyReplacementFingerprint,
} from '@/features/accounts/model/proxyReplacement';
import type { CodexInspectionResultItem } from '@/features/monitoring/codexInspection';
import type {
  CredentialProxyExitIPCheckResult,
  CredentialProxyExitIPTargetRequest,
  ProxyExitIPOutcome,
} from '@/services/api/usageService';

export interface ProxyHealthVerificationCandidate {
  item: CodexInspectionResultItem;
  // This value remains within the browser grouping operation. Returned groups
  // contain only a one-way fingerprint and strict credential locator.
  proxyURL: string;
  target: CredentialProxyExitIPTargetRequest;
}

export interface ProxyHealthVerificationGroup {
  fingerprint: string;
  representative: CredentialProxyExitIPTargetRequest;
  memberKeys: string[];
}

const VALID_OUTCOMES = new Set<ProxyExitIPOutcome>([
  'healthy',
  'proxy_failed',
  'inconclusive',
  'not_saved',
]);
const PROXY_URL_PATTERN = /https?:\/\/[^\s"'<>}\]]+/gi;
const MAX_REASON_LENGTH = 240;

const normalizeOutcome = (value: unknown): ProxyExitIPOutcome =>
  typeof value === 'string' && VALID_OUTCOMES.has(value as ProxyExitIPOutcome)
    ? (value as ProxyExitIPOutcome)
    : 'inconclusive';

const normalizeCheckedAtMs = (value: number): number =>
  Number.isFinite(value) && value > 0 ? Math.floor(value) : Date.now();

// The Manager returns reason codes, but keep the persistence boundary robust
// against a future transport implementation accidentally returning a URL.
export const sanitizeProxyHealthVerificationReason = (value: unknown): string =>
  String(value ?? '')
    .trim()
    .replace(PROXY_URL_PATTERN, '[redacted-url]')
    .slice(0, MAX_REASON_LENGTH);

export const hasValidCurrentProxyHealthVerification = (
  item: CodexInspectionResultItem,
  currentProxyURL: string
): boolean => {
  const fingerprint = proxyReplacementFingerprint(currentProxyURL);
  const outcome = item.proxyVerificationOutcome;
  return Boolean(
    fingerprint &&
      item.proxyVerificationSource === 'exit_ip' &&
      item.proxyVerificationFingerprint === fingerprint &&
      outcome &&
      VALID_OUTCOMES.has(outcome)
  );
};

// Groups represent all inspection rows sharing a current proxy. A group is
// emitted only if at least one member lacks proof for that exact current proxy;
// one strict locator then refreshes the evidence for every member.
export const collectProxyHealthVerificationGroups = (
  candidates: readonly ProxyHealthVerificationCandidate[]
): ProxyHealthVerificationGroup[] => {
  type PendingGroup = ProxyHealthVerificationGroup & { needsVerification: boolean };
  const grouped = new Map<string, PendingGroup>();
  const seenMemberKeys = new Set<string>();

  for (const candidate of candidates) {
    const memberKey = candidate.item.key.trim();
    const proxyURL = normalizeProxyReplacementURL(candidate.proxyURL);
    const fingerprint = proxyReplacementFingerprint(proxyURL);
    if (!memberKey || !proxyURL || !fingerprint || seenMemberKeys.has(memberKey)) continue;
    seenMemberKeys.add(memberKey);

    const target = {
      ...candidate.target,
      clientKey: memberKey,
    };
    let group = grouped.get(fingerprint);
    if (!group) {
      group = {
        fingerprint,
        representative: target,
        memberKeys: [],
        needsVerification: false,
      };
      grouped.set(fingerprint, group);
    }
    group.memberKeys.push(memberKey);
    if (!hasValidCurrentProxyHealthVerification(candidate.item, proxyURL)) {
      if (!group.needsVerification) {
        group.representative = target;
      }
      group.needsVerification = true;
    }
  }

  return Array.from(grouped.values())
    .filter((group) => group.needsVerification)
    .map(({ needsVerification: _needsVerification, ...group }) => group);
};

export const applyProxyHealthVerificationResults = (
  rows: readonly CodexInspectionResultItem[],
  groups: readonly ProxyHealthVerificationGroup[],
  checks: readonly CredentialProxyExitIPCheckResult[],
  checkedAtMs: number
): CodexInspectionResultItem[] => {
  const checkByRepresentativeKey = new Map(
    checks
      .map((check) => [check.clientKey.trim(), check] as const)
      .filter(([clientKey]) => Boolean(clientKey))
  );
  const updatesByMemberKey = new Map<
    string,
    Pick<
      CodexInspectionResultItem,
      | 'proxySuspect'
      | 'proxyVerificationSource'
      | 'proxyVerificationOutcome'
      | 'proxyVerificationReason'
      | 'proxyVerificationFingerprint'
      | 'proxyVerificationCheckedAtMs'
    >
  >();
  const normalizedCheckedAtMs = normalizeCheckedAtMs(checkedAtMs);

  for (const group of groups) {
    const check = checkByRepresentativeKey.get(group.representative.clientKey.trim());
    if (!check) continue;
    const outcome = normalizeOutcome(check.outcome);
    const reason = sanitizeProxyHealthVerificationReason(check.reason);
    const update = {
      proxySuspect: outcome === 'proxy_failed',
      proxyVerificationSource: 'exit_ip' as const,
      proxyVerificationOutcome: outcome,
      ...(reason ? { proxyVerificationReason: reason } : {}),
      proxyVerificationFingerprint: group.fingerprint,
      proxyVerificationCheckedAtMs: normalizedCheckedAtMs,
    };
    for (const memberKey of group.memberKeys) {
      updatesByMemberKey.set(memberKey, update);
    }
  }

  if (updatesByMemberKey.size === 0) return [...rows];
  return rows.map((row) => {
    const update = updatesByMemberKey.get(row.key.trim());
    if (!update) return row;
    return {
      ...row,
      ...update,
    };
  });
};
