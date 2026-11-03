import type { PluginConfigObject } from '@/types';
import { isRecord } from '@/utils/helpers';

export const RATE_LIMIT_BRIDGE_PROTOCOL = 'cpamp.per-auth-rate-limit/v1';
export const RATE_LIMIT_BRIDGE_PLUGIN_ID = 'per-auth-rate-limit';
export const RATE_LIMIT_BRIDGE_PARENT_ORIGIN_PARAM = 'cpamp_bridge_parent_origin';

const requestIDPattern = /^[A-Za-z0-9_-]{1,96}$/;
const durationPattern = /^(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+$/u;
const maxRPM = 2_147_483_647;
const maxPendingRequests = 2_147_483_647;
const maxConcurrentPerAuth = 2_147_483_647;
const maxConcurrentPerEgress = 2_147_483_647;
const defaultMaxQueueWait = '35s';
const defaultMaxPendingRequests = 500;
const defaultMaxConcurrentPerAuth = 0;
const defaultEgressConcurrencyEnabled = false;
const defaultMaxConcurrentPerEgress = 0;
const defaultEgressAccountPriorityAffinityEnabled = false;
const defaultAffinityWait = '15s';
const defaultAllCandidatesWait = '30s';
const defaultRetryInitialInterval = '250ms';
const defaultRetryMaxInterval = '1s';
const defaultRebindSessionAfterAffinityWait = true;
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
}

export type RateLimitBridgeRequest = RateLimitBridgeLoadRequest | RateLimitBridgeSaveRequest;

export type RateLimitBridgeResponse =
  | {
      protocol: typeof RATE_LIMIT_BRIDGE_PROTOCOL;
      pluginID: typeof RATE_LIMIT_BRIDGE_PLUGIN_ID;
      requestID: string;
      operation: 'result';
      ok: true;
      settings: RateLimitBridgeSettings;
    }
  | {
      protocol: typeof RATE_LIMIT_BRIDGE_PROTOCOL;
      pluginID: typeof RATE_LIMIT_BRIDGE_PLUGIN_ID;
      requestID: string;
      operation: 'result';
      ok: false;
      error: string;
    };

const isValidRequestID = (value: unknown): value is string =>
  typeof value === 'string' && requestIDPattern.test(value);

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

const hasOwn = (value: Record<string, unknown>, key: string): boolean =>
  Object.prototype.hasOwnProperty.call(value, key);

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
      typeof parsedEgressAccountPriorityAffinityEnabled !== 'boolean')
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
  };
};

export const createRateLimitBridgeSuccess = (
  request: RateLimitBridgeRequest,
  settings: RateLimitBridgeSettings
): RateLimitBridgeResponse => ({
  protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
  pluginID: RATE_LIMIT_BRIDGE_PLUGIN_ID,
  requestID: request.requestID,
  operation: 'result',
  ok: true,
  settings,
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
  reply: (source: MessageEventSource | null, response: RateLimitBridgeResponse) => void;
}

export const createRateLimitBridgeHost =
  (options: RateLimitBridgeHostOptions) => {
    let saveQueue = Promise.resolve();

    return async (event: RateLimitBridgeHostEvent): Promise<void> => {
      const request = parseTrustedRateLimitBridgeRequest(event.data, {
        sourceMatchesFrame: options.isActiveFrameSource(event.source),
        origin: event.origin,
        expectedOrigin: options.expectedOrigin,
      });
      if (!request) return;

      try {
        if (request.operation === 'load') {
          const config = await options.getConfig();
          options.reply(
            event.source,
            createRateLimitBridgeSuccess(request, normalizeRateLimitBridgeSettings(config))
          );
          return;
        }

        const saveTurn = saveQueue.then(async () => {
          const patch = buildRateLimitConfigPatch(request);
          const mergedPatch = request.legacy
            ? patch
            : preserveRateLimitConcurrencyConfig(patch, await options.getConfig());
          await options.patchConfig(mergedPatch);
        });
        saveQueue = saveTurn.catch(() => undefined);

        await saveTurn;
        options.reply(event.source, createRateLimitBridgeSuccess(request, request.settings));
      } catch {
        const error = request.operation === 'load' ? '无法读取限流设置' : '无法保存限流设置';
        options.reply(event.source, createRateLimitBridgeFailure(request, error));
      }
    };
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
