import type { PluginConfigObject } from '@/types';
import { isRecord } from '@/utils/helpers';

export const RATE_LIMIT_BRIDGE_PROTOCOL = 'cpamp.per-auth-rate-limit/v1';
export const RATE_LIMIT_BRIDGE_PLUGIN_ID = 'per-auth-rate-limit';
export const RATE_LIMIT_BRIDGE_PARENT_ORIGIN_PARAM = 'cpamp_bridge_parent_origin';
export const RATE_LIMIT_BRIDGE_MAX_PENDING_SAVES = 16;

const requestIDPattern = /^[A-Za-z0-9_-]{1,96}$/;
const durationPattern = /^(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+$/u;
const maxRPM = 2_147_483_647;
const maxPendingRequests = 2_147_483_647;
const maxConcurrentPerAuth = 2_147_483_647;
const maxConcurrentPerEgress = 2_147_483_647;
// Go's time.Duration is a signed int64 count of nanoseconds. Keep values written
// by the bridge within the largest whole-second duration the CPA plugin can parse.
const maxEgressRecoveryTTLSeconds = 9_223_372_036;
const egressRecoveryTTLPattern = /^(0|[1-9]\d*)s$/;
const defaultMaxQueueWait = '35s';
const defaultMaxPendingRequests = 500;
const defaultMaxConcurrentPerAuth = 0;
const defaultEgressConcurrencyEnabled = false;
const defaultMaxConcurrentPerEgress = 0;
const defaultEgressAccountPriorityAffinityEnabled = false;
const defaultEgressAuthRecoveryTTL = '900s';
const defaultEgress502HandoffTTL = '30s';
const defaultEgress429HandoffTTL = '120s';
const defaultAffinityWait = '15s';
const defaultAllCandidatesWait = '30s';
const defaultRetryInitialInterval = '250ms';
const defaultRetryMaxInterval = '1s';
const defaultRebindSessionAfterAffinityWait = true;
const defaultEgressDiagnosticsLimit = 100;
const maxEgressDiagnosticsLimit = 1024;
const maxEgressDiagnosticBindings = 4096;
const maxEgressDiagnosticStringLength = 512;
const maxEgressDiagnosticReasonLength = 128;
const maxEgressDiagnosticInFlight = 4_294_967_295;
const accountConcurrencySettingKeys = [
  'concurrencyEnabled',
  'maxConcurrentPerAuth',
  'affinityWait',
  'allCandidatesWait',
  'retryInitialInterval',
  'retryMaxInterval',
  'rebindSessionAfterAffinityWait',
] as const;
const egressConcurrencySettingKeys = [
  'egressConcurrencyEnabled',
  'maxConcurrentPerEgress',
] as const;
const egressAccountPriorityAffinitySettingKey = 'egressAccountPriorityAffinityEnabled';
const egressRecoverySettingKeys = [
  'egressAuthRecoveryTTL',
  'egress502HandoffTTL',
  'egress429HandoffTTL',
] as const;

export interface RateLimitBridgeSettings {
  enabled: boolean;
  rpm: number;
  minInterval: string;
  maxQueueWait: string;
  maxPendingRequests: number;
  concurrencyEnabled: boolean;
  maxConcurrentPerAuth: number;
  affinityWait: string;
  allCandidatesWait: string;
  retryInitialInterval: string;
  retryMaxInterval: string;
  rebindSessionAfterAffinityWait: boolean;
  egressConcurrencyEnabled: boolean;
  maxConcurrentPerEgress: number;
  egressAccountPriorityAffinityEnabled: boolean;
  egressAuthRecoveryTTL: string;
  egress502HandoffTTL: string;
  egress429HandoffTTL: string;
}

export interface RateLimitBridgeLoadRequest {
  protocol: typeof RATE_LIMIT_BRIDGE_PROTOCOL;
  pluginID: typeof RATE_LIMIT_BRIDGE_PLUGIN_ID;
  requestID: string;
  operation: 'load';
}

export interface RateLimitBridgeSaveRequest {
  protocol: typeof RATE_LIMIT_BRIDGE_PROTOCOL;
  pluginID: typeof RATE_LIMIT_BRIDGE_PLUGIN_ID;
  requestID: string;
  operation: 'save';
  settings: RateLimitBridgeSettings;
  /** True when the producer only knows the original v1 settings schema. */
  legacy: boolean;
  /** True when the producer supplied both egress concurrency settings. */
  hasEgressConcurrencySettings: boolean;
  /** True when the producer understands and explicitly set the priority-affinity toggle. */
  hasEgressAccountPriorityAffinitySetting: boolean;
  /** True when the producer supplied all three egress recovery handoff TTLs. */
  hasEgressRecoverySettings: boolean;
}

export interface RateLimitBridgeDiagnosticsRequest {
  protocol: typeof RATE_LIMIT_BRIDGE_PROTOCOL;
  pluginID: typeof RATE_LIMIT_BRIDGE_PLUGIN_ID;
  requestID: string;
  operation: 'diagnostics';
  limit: number;
}

export type RateLimitBridgeRequest =
  | RateLimitBridgeLoadRequest
  | RateLimitBridgeSaveRequest
  | RateLimitBridgeDiagnosticsRequest;

export interface RateLimitEgressDiagnosticBinding {
  egressHost: string;
  ownerAuthID?: string;
  holdKind?: string;
  holdUntil?: string;
  updatedAt: string;
  inFlight: number;
  releaseAfterInFlight?: boolean;
}

export interface RateLimitEgressDiagnosticEvent {
  at: string;
  event: string;
  reason?: string;
  egressHost?: string;
  authID?: string;
  holdKind?: string;
  holdUntil?: string;
}

export interface RateLimitEgressDiagnostics {
  generatedAt: string;
  bindings: RateLimitEgressDiagnosticBinding[];
  events: RateLimitEgressDiagnosticEvent[];
}

type RateLimitBridgeSettingsSuccess = {
  protocol: typeof RATE_LIMIT_BRIDGE_PROTOCOL;
  pluginID: typeof RATE_LIMIT_BRIDGE_PLUGIN_ID;
  requestID: string;
  operation: 'result';
  ok: true;
  settings: RateLimitBridgeSettings;
};

type RateLimitBridgeDiagnosticsSuccess = {
  protocol: typeof RATE_LIMIT_BRIDGE_PROTOCOL;
  pluginID: typeof RATE_LIMIT_BRIDGE_PLUGIN_ID;
  requestID: string;
  operation: 'result';
  ok: true;
  diagnostics: RateLimitEgressDiagnostics;
};

type RateLimitBridgeFailure = {
  protocol: typeof RATE_LIMIT_BRIDGE_PROTOCOL;
  pluginID: typeof RATE_LIMIT_BRIDGE_PLUGIN_ID;
  requestID: string;
  operation: 'result';
  ok: false;
  error: string;
};

export type RateLimitBridgeResponse =
  | RateLimitBridgeSettingsSuccess
  | RateLimitBridgeDiagnosticsSuccess
  | RateLimitBridgeFailure;

const isValidRequestID = (value: unknown): value is string =>
  typeof value === 'string' && requestIDPattern.test(value);

const isValidDiagnosticsLimit = (value: unknown): value is number =>
  typeof value === 'number' &&
  Number.isSafeInteger(value) &&
  value >= 1 &&
  value <= maxEgressDiagnosticsLimit;

const isValidRPM = (value: unknown): value is number =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 && value <= maxRPM;

const isValidPendingRequests = (value: unknown): value is number =>
  typeof value === 'number' &&
  Number.isSafeInteger(value) &&
  value >= 0 &&
  value <= maxPendingRequests;

const isValidMaxConcurrentPerAuth = (value: unknown): value is number =>
  typeof value === 'number' &&
  Number.isSafeInteger(value) &&
  value >= 0 &&
  value <= maxConcurrentPerAuth;

const isValidMaxConcurrentPerEgress = (value: unknown): value is number =>
  typeof value === 'number' &&
  Number.isSafeInteger(value) &&
  value >= 0 &&
  value <= maxConcurrentPerEgress;

const isValidDuration = (value: unknown): value is string =>
  typeof value === 'string' && value.length <= 64 && durationPattern.test(value.trim());

// The iframe saves these three fields in canonical whole seconds. Do not accept
// generic Go duration syntax here: the plugin intentionally rejects sub-second
// handoff values, and a duration above this bound overflows Go time.Duration.
const isValidEgressRecoveryTTL = (value: unknown): value is string => {
  if (typeof value !== 'string' || value.length > 64) return false;
  const match = egressRecoveryTTLPattern.exec(value.trim());
  if (!match) return false;
  const seconds = Number(match[1]);
  return Number.isSafeInteger(seconds) && seconds >= 0 && seconds <= maxEgressRecoveryTTLSeconds;
};

const hasOwn = (value: Record<string, unknown>, key: string): boolean =>
  Object.prototype.hasOwnProperty.call(value, key);

const diagnosticEventNames = new Set([
  'owner_pending',
  'owner_bound',
  'failure_hold',
  'owner_recovered',
  'owner_released',
]);

const diagnosticHoldKinds = new Set(['auth_recovery', 'overload_502', 'quota_429']);

const normalizeDiagnosticText = (value: unknown, maximumLength: number): string => {
  if (typeof value !== 'string') return '';
  const normalized = value.trim();
  if (
    !normalized ||
    normalized.length > maximumLength ||
    /[\u0000-\u001f\u007f]/u.test(normalized)
  ) {
    return '';
  }
  return normalized;
};

const normalizeDiagnosticTimestamp = (value: unknown): string => {
  const normalized = normalizeDiagnosticText(value, 64);
  return normalized && Number.isFinite(Date.parse(normalized)) ? normalized : '';
};

const normalizeDiagnosticEgressHost = (value: unknown): string => {
  const normalized = normalizeDiagnosticText(value, 256);
  if (!normalized || /[@\/\\\s]/u.test(normalized)) return '';

  if (normalized.includes(':')) {
    if (normalized.indexOf(':') === normalized.lastIndexOf(':')) return '';
    try {
      new URL('http://[' + normalized + ']');
      return normalized;
    } catch {
      return '';
    }
  }

  return /^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,254}[A-Za-z0-9])?$/u.test(normalized)
    ? normalized
    : '';
};

const normalizeDiagnosticHoldKind = (value: unknown): string => {
  const normalized = normalizeDiagnosticText(value, 64);
  return diagnosticHoldKinds.has(normalized) ? normalized : '';
};

const normalizeDiagnosticEventName = (value: unknown): string => {
  const normalized = normalizeDiagnosticText(value, 64);
  return diagnosticEventNames.has(normalized) ? normalized : '';
};

const normalizeRateLimitEgressDiagnosticBinding = (
  value: unknown
): RateLimitEgressDiagnosticBinding | null => {
  if (!isRecord(value)) return null;
  const egressHost = normalizeDiagnosticEgressHost(value.egress_host ?? value.egressHost);
  const updatedAt = normalizeDiagnosticTimestamp(value.updated_at ?? value.updatedAt);
  const inFlight = value.in_flight ?? value.inFlight;
  if (
    !egressHost ||
    !updatedAt ||
    typeof inFlight !== 'number' ||
    !Number.isSafeInteger(inFlight) ||
    inFlight < 0 ||
    inFlight > maxEgressDiagnosticInFlight
  ) {
    return null;
  }

  const binding: RateLimitEgressDiagnosticBinding = { egressHost, updatedAt, inFlight };
  const ownerAuthID = normalizeDiagnosticText(value.owner_auth_id ?? value.ownerAuthID, maxEgressDiagnosticStringLength);
  const holdKind = normalizeDiagnosticHoldKind(value.hold_kind ?? value.holdKind);
  const holdUntil = normalizeDiagnosticTimestamp(value.hold_until ?? value.holdUntil);
  if (ownerAuthID) binding.ownerAuthID = ownerAuthID;
  if (holdKind) binding.holdKind = holdKind;
  if (holdUntil) binding.holdUntil = holdUntil;
  if (value.release_after_in_flight === true || value.releaseAfterInFlight === true) {
    binding.releaseAfterInFlight = true;
  }
  return binding;
};

const normalizeRateLimitEgressDiagnosticEvent = (
  value: unknown
): RateLimitEgressDiagnosticEvent | null => {
  if (!isRecord(value)) return null;
  const at = normalizeDiagnosticTimestamp(value.at);
  const event = normalizeDiagnosticEventName(value.event);
  if (!at || !event) return null;

  const result: RateLimitEgressDiagnosticEvent = { at, event };
  const reason = normalizeDiagnosticText(value.reason, maxEgressDiagnosticReasonLength);
  const egressHost = normalizeDiagnosticEgressHost(value.egress_host ?? value.egressHost);
  const authID = normalizeDiagnosticText(value.auth_id ?? value.authID, maxEgressDiagnosticStringLength);
  const holdKind = normalizeDiagnosticHoldKind(value.hold_kind ?? value.holdKind);
  const holdUntil = normalizeDiagnosticTimestamp(value.hold_until ?? value.holdUntil);
  if (reason) result.reason = reason;
  if (egressHost) result.egressHost = egressHost;
  if (authID) result.authID = authID;
  if (holdKind) result.holdKind = holdKind;
  if (holdUntil) result.holdUntil = holdUntil;
  return result;
};

export const normalizeRateLimitEgressDiagnostics = (
  value: unknown,
  limit: number
): RateLimitEgressDiagnostics | null => {
  if (!isRecord(value) || !isValidDiagnosticsLimit(limit)) return null;
  const generatedAt = normalizeDiagnosticTimestamp(value.generated_at ?? value.generatedAt);
  if (!generatedAt) return null;

  const bindings = Array.isArray(value.bindings)
    ? value.bindings
        .slice(0, maxEgressDiagnosticBindings)
        .map((entry) => normalizeRateLimitEgressDiagnosticBinding(entry))
        .filter((entry): entry is RateLimitEgressDiagnosticBinding => entry !== null)
    : [];
  const events = Array.isArray(value.events)
    ? value.events
        .slice(-maxEgressDiagnosticsLimit)
        .map((entry) => normalizeRateLimitEgressDiagnosticEvent(entry))
        .filter((entry): entry is RateLimitEgressDiagnosticEvent => entry !== null)
        .slice(-limit)
    : [];

  return { generatedAt, bindings, events };
};

const parseSettings = (value: unknown): RateLimitBridgeSettings | null => {
  if (!isRecord(value)) return null;
  const hasAnyAccountConcurrencySetting = accountConcurrencySettingKeys.some((key) =>
    hasOwn(value, key)
  );
  const hasEveryAccountConcurrencySetting = accountConcurrencySettingKeys.every((key) =>
    hasOwn(value, key)
  );
  const hasAnyEgressConcurrencySetting = egressConcurrencySettingKeys.some((key) =>
    hasOwn(value, key)
  );
  const hasEveryEgressConcurrencySetting = egressConcurrencySettingKeys.every((key) =>
    hasOwn(value, key)
  );
  const hasEgressAccountPriorityAffinitySetting = hasOwn(
    value,
    egressAccountPriorityAffinitySettingKey
  );
  const hasAnyEgressRecoverySetting = egressRecoverySettingKeys.some((key) => hasOwn(value, key));
  const hasEveryEgressRecoverySetting = egressRecoverySettingKeys.every((key) =>
    hasOwn(value, key)
  );
  if (hasAnyAccountConcurrencySetting && !hasEveryAccountConcurrencySetting) return null;
  if (
    hasAnyEgressConcurrencySetting &&
    (!hasEveryAccountConcurrencySetting || !hasEveryEgressConcurrencySetting)
  ) {
    return null;
  }
  if (
    hasEgressAccountPriorityAffinitySetting &&
    (!hasEveryAccountConcurrencySetting || !hasEveryEgressConcurrencySetting)
  ) {
    return null;
  }
  if (
    hasAnyEgressRecoverySetting &&
    (!hasEveryAccountConcurrencySetting ||
      !hasEveryEgressConcurrencySetting ||
      !hasEveryEgressRecoverySetting)
  ) {
    return null;
  }
  const legacy = !hasAnyAccountConcurrencySetting;
  const parsedEnabled = value.enabled;
  const parsedRPM = value.rpm;
  const parsedPendingRequests = value.maxPendingRequests;
  const parsedConcurrencyEnabled = value.concurrencyEnabled;
  const parsedMaxConcurrentPerAuth = value.maxConcurrentPerAuth;
  const parsedRebindSessionAfterAffinityWait = value.rebindSessionAfterAffinityWait;
  const parsedEgressConcurrencyEnabled = value.egressConcurrencyEnabled;
  const parsedMaxConcurrentPerEgress = value.maxConcurrentPerEgress;
  const parsedEgressAccountPriorityAffinityEnabled = value.egressAccountPriorityAffinityEnabled;
  const minInterval = typeof value.minInterval === 'string' ? value.minInterval.trim() : '';
  const maxQueueWait = typeof value.maxQueueWait === 'string' ? value.maxQueueWait.trim() : '';
  const affinityWait = typeof value.affinityWait === 'string' ? value.affinityWait.trim() : '';
  const allCandidatesWait =
    typeof value.allCandidatesWait === 'string' ? value.allCandidatesWait.trim() : '';
  const retryInitialInterval =
    typeof value.retryInitialInterval === 'string' ? value.retryInitialInterval.trim() : '';
  const retryMaxInterval =
    typeof value.retryMaxInterval === 'string' ? value.retryMaxInterval.trim() : '';
  const egressAuthRecoveryTTL =
    typeof value.egressAuthRecoveryTTL === 'string' ? value.egressAuthRecoveryTTL.trim() : '';
  const egress502HandoffTTL =
    typeof value.egress502HandoffTTL === 'string' ? value.egress502HandoffTTL.trim() : '';
  const egress429HandoffTTL =
    typeof value.egress429HandoffTTL === 'string' ? value.egress429HandoffTTL.trim() : '';
  if (
    typeof parsedEnabled !== 'boolean' ||
    !isValidRPM(parsedRPM) ||
    !isValidDuration(minInterval) ||
    !isValidDuration(maxQueueWait) ||
    !isValidPendingRequests(parsedPendingRequests) ||
    (!legacy &&
      (typeof parsedConcurrencyEnabled !== 'boolean' ||
        !isValidMaxConcurrentPerAuth(parsedMaxConcurrentPerAuth) ||
        !isValidDuration(affinityWait) ||
        !isValidDuration(allCandidatesWait) ||
        !isValidDuration(retryInitialInterval) ||
        !isValidDuration(retryMaxInterval) ||
        typeof parsedRebindSessionAfterAffinityWait !== 'boolean')) ||
    (hasAnyEgressConcurrencySetting &&
      (typeof parsedEgressConcurrencyEnabled !== 'boolean' ||
        !isValidMaxConcurrentPerEgress(parsedMaxConcurrentPerEgress))) ||
    (hasEgressAccountPriorityAffinitySetting &&
      typeof parsedEgressAccountPriorityAffinityEnabled !== 'boolean') ||
    (hasAnyEgressRecoverySetting &&
      (!isValidEgressRecoveryTTL(egressAuthRecoveryTTL) ||
        !isValidEgressRecoveryTTL(egress502HandoffTTL) ||
        !isValidEgressRecoveryTTL(egress429HandoffTTL)))
  ) {
    return null;
  }
  const normalizedConcurrencyEnabled = legacy ? false : parsedConcurrencyEnabled === true;
  const normalizedMaxConcurrentPerAuth = legacy
    ? defaultMaxConcurrentPerAuth
    : isValidMaxConcurrentPerAuth(parsedMaxConcurrentPerAuth)
      ? parsedMaxConcurrentPerAuth
      : defaultMaxConcurrentPerAuth;
  const normalizedRebindSessionAfterAffinityWait = legacy
    ? defaultRebindSessionAfterAffinityWait
    : parsedRebindSessionAfterAffinityWait === true;
  const normalizedEgressConcurrencyEnabled = hasAnyEgressConcurrencySetting
    ? parsedEgressConcurrencyEnabled === true
    : defaultEgressConcurrencyEnabled;
  const normalizedMaxConcurrentPerEgress =
    hasAnyEgressConcurrencySetting && isValidMaxConcurrentPerEgress(parsedMaxConcurrentPerEgress)
      ? parsedMaxConcurrentPerEgress
      : defaultMaxConcurrentPerEgress;
  const normalizedEgressAccountPriorityAffinityEnabled = hasEgressAccountPriorityAffinitySetting
    ? parsedEgressAccountPriorityAffinityEnabled === true
    : defaultEgressAccountPriorityAffinityEnabled;
  const normalizedEgressAuthRecoveryTTL =
    hasAnyEgressRecoverySetting && isValidEgressRecoveryTTL(egressAuthRecoveryTTL)
      ? egressAuthRecoveryTTL
      : defaultEgressAuthRecoveryTTL;
  const normalizedEgress502HandoffTTL =
    hasAnyEgressRecoverySetting && isValidEgressRecoveryTTL(egress502HandoffTTL)
      ? egress502HandoffTTL
      : defaultEgress502HandoffTTL;
  const normalizedEgress429HandoffTTL =
    hasAnyEgressRecoverySetting && isValidEgressRecoveryTTL(egress429HandoffTTL)
      ? egress429HandoffTTL
      : defaultEgress429HandoffTTL;
  if (
    normalizedEgressAccountPriorityAffinityEnabled &&
    (!normalizedConcurrencyEnabled ||
      !normalizedEgressConcurrencyEnabled ||
      normalizedMaxConcurrentPerEgress <= 0)
  ) {
    return null;
  }
  return {
    enabled: parsedEnabled,
    rpm: parsedRPM,
    minInterval,
    maxQueueWait,
    maxPendingRequests: parsedPendingRequests,
    concurrencyEnabled: normalizedConcurrencyEnabled,
    maxConcurrentPerAuth: normalizedMaxConcurrentPerAuth,
    affinityWait: legacy ? defaultAffinityWait : affinityWait,
    allCandidatesWait: legacy ? defaultAllCandidatesWait : allCandidatesWait,
    retryInitialInterval: legacy ? defaultRetryInitialInterval : retryInitialInterval,
    retryMaxInterval: legacy ? defaultRetryMaxInterval : retryMaxInterval,
    rebindSessionAfterAffinityWait: normalizedRebindSessionAfterAffinityWait,
    egressConcurrencyEnabled: normalizedEgressConcurrencyEnabled,
    maxConcurrentPerEgress: normalizedMaxConcurrentPerEgress,
    egressAccountPriorityAffinityEnabled: normalizedEgressAccountPriorityAffinityEnabled,
    egressAuthRecoveryTTL: normalizedEgressAuthRecoveryTTL,
    egress502HandoffTTL: normalizedEgress502HandoffTTL,
    egress429HandoffTTL: normalizedEgress429HandoffTTL,
  };
};

export const parseRateLimitBridgeRequest = (value: unknown): RateLimitBridgeRequest | null => {
  if (!isRecord(value)) return null;
  if (
    value.protocol !== RATE_LIMIT_BRIDGE_PROTOCOL ||
    value.pluginID !== RATE_LIMIT_BRIDGE_PLUGIN_ID ||
    !isValidRequestID(value.requestID)
  ) {
    return null;
  }
  if (value.operation === 'load') {
    return {
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: RATE_LIMIT_BRIDGE_PLUGIN_ID,
      requestID: value.requestID,
      operation: 'load',
    };
  }
  if (value.operation === 'diagnostics') {
    const limit = hasOwn(value, 'limit') ? value.limit : defaultEgressDiagnosticsLimit;
    return isValidDiagnosticsLimit(limit)
      ? {
          protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
          pluginID: RATE_LIMIT_BRIDGE_PLUGIN_ID,
          requestID: value.requestID,
          operation: 'diagnostics',
          limit,
        }
      : null;
  }
  if (value.operation !== 'save') return null;
  const settingsValue = value.settings;
  const settings = parseSettings(settingsValue);
  const legacy = isRecord(settingsValue)
    ? !accountConcurrencySettingKeys.some((key) => hasOwn(settingsValue, key))
    : false;
  const hasEgressConcurrencySettings = isRecord(settingsValue)
    ? egressConcurrencySettingKeys.every((key) => hasOwn(settingsValue, key))
    : false;
  const hasEgressAccountPriorityAffinitySetting = isRecord(settingsValue)
    ? hasOwn(settingsValue, egressAccountPriorityAffinitySettingKey)
    : false;
  const hasEgressRecoverySettings = isRecord(settingsValue)
    ? egressRecoverySettingKeys.every((key) => hasOwn(settingsValue, key))
    : false;
  return settings
    ? {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: RATE_LIMIT_BRIDGE_PLUGIN_ID,
        requestID: value.requestID,
        operation: 'save',
        settings,
        legacy,
        hasEgressConcurrencySettings,
        hasEgressAccountPriorityAffinitySetting,
        hasEgressRecoverySettings,
      }
    : null;
};

export const parseTrustedRateLimitBridgeRequest = (
  value: unknown,
  trust: { sourceMatchesFrame: boolean; origin: string; expectedOrigin: string }
): RateLimitBridgeRequest | null => {
  if (!trust.sourceMatchesFrame || !trust.expectedOrigin || trust.origin !== trust.expectedOrigin) {
    return null;
  }
  return parseRateLimitBridgeRequest(value);
};

export const buildRateLimitConfigPatch = (
  request: RateLimitBridgeSaveRequest
): PluginConfigObject => {
  const defaultPatch: PluginConfigObject = {
    rpm: request.settings.rpm,
    min_interval: request.settings.minInterval,
  };
  if (!request.legacy) {
    defaultPatch.max_concurrent_per_auth = request.settings.maxConcurrentPerAuth;
  }

  const concurrencyPatch: PluginConfigObject = {
    enabled: request.settings.concurrencyEnabled,
    affinity_wait: request.settings.affinityWait,
    all_candidates_wait: request.settings.allCandidatesWait,
    retry_initial_interval: request.settings.retryInitialInterval,
    retry_max_interval: request.settings.retryMaxInterval,
    rebind_session_after_affinity_wait: request.settings.rebindSessionAfterAffinityWait,
  };
  if (request.hasEgressConcurrencySettings) {
    concurrencyPatch.egress_enabled = request.settings.egressConcurrencyEnabled;
    concurrencyPatch.max_concurrent_per_egress = request.settings.maxConcurrentPerEgress;
  }
  if (request.hasEgressAccountPriorityAffinitySetting) {
    concurrencyPatch.egress_account_priority_affinity_enabled =
      request.settings.egressAccountPriorityAffinityEnabled;
  }
  if (request.hasEgressRecoverySettings) {
    concurrencyPatch.egress_auth_recovery_ttl = request.settings.egressAuthRecoveryTTL;
    concurrencyPatch.egress_502_handoff_ttl = request.settings.egress502HandoffTTL;
    concurrencyPatch.egress_429_handoff_ttl = request.settings.egress429HandoffTTL;
  }

  return {
    enabled: request.settings.enabled,
    default: defaultPatch,
    max_queue_wait: request.settings.maxQueueWait,
    max_pending_requests: request.settings.maxPendingRequests,
    ...(request.legacy
      ? {}
      : {
          concurrency: concurrencyPatch,
        }),
    accounts: null,
  };
};

/**
 * CPA's plugin PATCH endpoint merges the top level only. Keep unknown/current
 * nested concurrency fields when an older iframe does not send the egress
 * fields introduced by the concurrency scheduler.
 */
export const preserveRateLimitConcurrencyConfig = (
  patch: PluginConfigObject,
  current: PluginConfigObject
): PluginConfigObject => {
  const patchConcurrency = isRecord(patch.concurrency) ? patch.concurrency : null;
  const currentConcurrency = isRecord(current.concurrency) ? current.concurrency : null;
  if (!patchConcurrency || !currentConcurrency) return patch;
  return {
    ...patch,
    concurrency: {
      ...currentConcurrency,
      ...patchConcurrency,
    },
  };
};

export const normalizeRateLimitBridgeSettings = (
  config: PluginConfigObject
): RateLimitBridgeSettings => {
  const defaultPolicy = isRecord(config.default) ? config.default : {};
  const concurrency = isRecord(config.concurrency) ? config.concurrency : {};
  const rpm = isValidRPM(defaultPolicy.rpm) ? defaultPolicy.rpm : 0;
  const candidateInterval =
    typeof defaultPolicy.min_interval === 'string' ? defaultPolicy.min_interval.trim() : '';
  const candidateQueueWait =
    typeof config.max_queue_wait === 'string' ? config.max_queue_wait.trim() : '';
  const candidateAffinityWait =
    typeof concurrency.affinity_wait === 'string' ? concurrency.affinity_wait.trim() : '';
  const candidateAllCandidatesWait =
    typeof concurrency.all_candidates_wait === 'string'
      ? concurrency.all_candidates_wait.trim()
      : '';
  const candidateRetryInitialInterval =
    typeof concurrency.retry_initial_interval === 'string'
      ? concurrency.retry_initial_interval.trim()
      : '';
  const candidateRetryMaxInterval =
    typeof concurrency.retry_max_interval === 'string' ? concurrency.retry_max_interval.trim() : '';
  const candidateEgressAuthRecoveryTTL =
    typeof concurrency.egress_auth_recovery_ttl === 'string'
      ? concurrency.egress_auth_recovery_ttl.trim()
      : '';
  const candidateEgress502HandoffTTL =
    typeof concurrency.egress_502_handoff_ttl === 'string'
      ? concurrency.egress_502_handoff_ttl.trim()
      : '';
  const candidateEgress429HandoffTTL =
    typeof concurrency.egress_429_handoff_ttl === 'string'
      ? concurrency.egress_429_handoff_ttl.trim()
      : '';

  return {
    enabled: config.enabled !== false,
    rpm,
    minInterval: isValidDuration(candidateInterval) ? candidateInterval : '0s',
    maxQueueWait: isValidDuration(candidateQueueWait) ? candidateQueueWait : defaultMaxQueueWait,
    maxPendingRequests: isValidPendingRequests(config.max_pending_requests)
      ? config.max_pending_requests
      : defaultMaxPendingRequests,
    concurrencyEnabled: concurrency.enabled === true,
    maxConcurrentPerAuth: isValidMaxConcurrentPerAuth(defaultPolicy.max_concurrent_per_auth)
      ? defaultPolicy.max_concurrent_per_auth
      : defaultMaxConcurrentPerAuth,
    affinityWait: isValidDuration(candidateAffinityWait)
      ? candidateAffinityWait
      : defaultAffinityWait,
    allCandidatesWait: isValidDuration(candidateAllCandidatesWait)
      ? candidateAllCandidatesWait
      : defaultAllCandidatesWait,
    retryInitialInterval: isValidDuration(candidateRetryInitialInterval)
      ? candidateRetryInitialInterval
      : defaultRetryInitialInterval,
    retryMaxInterval: isValidDuration(candidateRetryMaxInterval)
      ? candidateRetryMaxInterval
      : defaultRetryMaxInterval,
    rebindSessionAfterAffinityWait:
      typeof concurrency.rebind_session_after_affinity_wait === 'boolean'
        ? concurrency.rebind_session_after_affinity_wait
        : defaultRebindSessionAfterAffinityWait,
    egressConcurrencyEnabled: concurrency.egress_enabled === true,
    maxConcurrentPerEgress: isValidMaxConcurrentPerEgress(concurrency.max_concurrent_per_egress)
      ? concurrency.max_concurrent_per_egress
      : defaultMaxConcurrentPerEgress,
    egressAccountPriorityAffinityEnabled:
      concurrency.egress_account_priority_affinity_enabled === true,
    egressAuthRecoveryTTL: isValidDuration(candidateEgressAuthRecoveryTTL)
      ? candidateEgressAuthRecoveryTTL
      : defaultEgressAuthRecoveryTTL,
    egress502HandoffTTL: isValidDuration(candidateEgress502HandoffTTL)
      ? candidateEgress502HandoffTTL
      : defaultEgress502HandoffTTL,
    egress429HandoffTTL: isValidDuration(candidateEgress429HandoffTTL)
      ? candidateEgress429HandoffTTL
      : defaultEgress429HandoffTTL,
  };
};

export const createRateLimitBridgeSuccess = (
  request: RateLimitBridgeLoadRequest | RateLimitBridgeSaveRequest,
  settings: RateLimitBridgeSettings
): RateLimitBridgeResponse => ({
  protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
  pluginID: RATE_LIMIT_BRIDGE_PLUGIN_ID,
  requestID: request.requestID,
  operation: 'result',
  ok: true,
  settings,
});

export const createRateLimitBridgeDiagnosticsSuccess = (
  request: RateLimitBridgeDiagnosticsRequest,
  diagnostics: RateLimitEgressDiagnostics
): RateLimitBridgeResponse => ({
  protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
  pluginID: RATE_LIMIT_BRIDGE_PLUGIN_ID,
  requestID: request.requestID,
  operation: 'result',
  ok: true,
  diagnostics,
});

export const createRateLimitBridgeFailure = (
  request: RateLimitBridgeRequest,
  error: string
): RateLimitBridgeResponse => ({
  protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
  pluginID: RATE_LIMIT_BRIDGE_PLUGIN_ID,
  requestID: request.requestID,
  operation: 'result',
  ok: false,
  error,
});

export interface RateLimitBridgeHostEvent {
  data: unknown;
  origin: string;
  source: MessageEventSource | null;
}

export interface RateLimitBridgeHostOptions {
  expectedOrigin: string;
  isActiveFrameSource: (source: MessageEventSource | null) => boolean;
  getConfig: () => Promise<PluginConfigObject>;
  patchConfig: (patch: PluginConfigObject) => Promise<unknown>;
  getDiagnostics?: (limit: number) => Promise<unknown>;
  reply: (source: MessageEventSource | null, response: RateLimitBridgeResponse) => void;
}

export interface RateLimitBridgeHostHandler {
  (event: RateLimitBridgeHostEvent): Promise<void>;
  dispose(): void;
}

export const createRateLimitBridgeHost =
  (options: RateLimitBridgeHostOptions): RateLimitBridgeHostHandler => {
    let saveQueue = Promise.resolve();
    let pendingSaveCount = 0;
    let disposed = false;

    const handle: RateLimitBridgeHostHandler = async (event): Promise<void> => {
      if (disposed) return;

      const request = parseTrustedRateLimitBridgeRequest(event.data, {
        sourceMatchesFrame: options.isActiveFrameSource(event.source),
        origin: event.origin,
        expectedOrigin: options.expectedOrigin,
      });
      if (!request) return;

      if (request.operation === 'diagnostics') {
        if (!options.getDiagnostics) {
          options.reply(event.source, createRateLimitBridgeFailure(request, '出口绑定监控不可用'));
          return;
        }
        try {
          const diagnostics = normalizeRateLimitEgressDiagnostics(
            await options.getDiagnostics(request.limit),
            request.limit
          );
          if (disposed) return;
          if (!diagnostics) {
            options.reply(event.source, createRateLimitBridgeFailure(request, '出口绑定监控数据无效'));
            return;
          }
          options.reply(
            event.source,
            createRateLimitBridgeDiagnosticsSuccess(request, diagnostics)
          );
        } catch {
          if (disposed) return;
          options.reply(event.source, createRateLimitBridgeFailure(request, '无法读取出口绑定监控'));
        }
        return;
      }

      if (request.operation === 'load') {
        try {
          const config = await options.getConfig();
          if (disposed) return;
          options.reply(
            event.source,
            createRateLimitBridgeSuccess(request, normalizeRateLimitBridgeSettings(config))
          );
        } catch {
          if (disposed) return;
          options.reply(event.source, createRateLimitBridgeFailure(request, '无法读取限流设置'));
        }
        return;
      }

      if (pendingSaveCount >= RATE_LIMIT_BRIDGE_MAX_PENDING_SAVES) {
        if (!disposed) {
          options.reply(event.source, createRateLimitBridgeFailure(request, '无法保存限流设置'));
        }
        return;
      }
      pendingSaveCount += 1;

      const saveTurn = saveQueue.then(async () => {
        if (disposed) return;

        const patch = buildRateLimitConfigPatch(request);
        if (disposed) return;

        const mergedPatch = request.legacy
          ? patch
          : preserveRateLimitConcurrencyConfig(patch, await options.getConfig());
        if (disposed) return;

        await options.patchConfig(mergedPatch);
      });
      saveQueue = saveTurn.catch(() => undefined);

      try {
        await saveTurn;
        if (disposed) return;
        options.reply(event.source, createRateLimitBridgeSuccess(request, request.settings));
      } catch {
        if (disposed) return;
        options.reply(event.source, createRateLimitBridgeFailure(request, '无法保存限流设置'));
      } finally {
        pendingSaveCount -= 1;
      }
    };

    handle.dispose = () => {
      if (disposed) return;
      disposed = true;
    };

    return handle;
  };

const isHTTPOrigin = (url: URL): boolean => url.protocol === 'http:' || url.protocol === 'https:';

export const appendRateLimitBridgeParentOrigin = (source: string, parentOrigin: string): string => {
  try {
    const parentURL = new URL(parentOrigin);
    const resourceURL = new URL(source, parentURL.origin);
    if (!isHTTPOrigin(parentURL) || !isHTTPOrigin(resourceURL)) return source;
    resourceURL.searchParams.set(RATE_LIMIT_BRIDGE_PARENT_ORIGIN_PARAM, parentURL.origin);
    return resourceURL.toString();
  } catch {
    return source;
  }
};

export const resolveRateLimitBridgeFrameOrigin = (source: string, parentOrigin: string): string => {
  try {
    const resourceURL = new URL(source, parentOrigin);
    return isHTTPOrigin(resourceURL) ? resourceURL.origin : '';
  } catch {
    return '';
  }
};
