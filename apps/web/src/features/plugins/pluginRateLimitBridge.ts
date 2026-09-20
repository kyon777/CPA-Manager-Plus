import type { PluginConfigObject } from '@/types';
import { isRecord } from '@/utils/helpers';

export const RATE_LIMIT_BRIDGE_PROTOCOL = 'cpamp.per-auth-rate-limit/v1';
export const RATE_LIMIT_BRIDGE_PLUGIN_ID = 'per-auth-rate-limit';
export const RATE_LIMIT_BRIDGE_PARENT_ORIGIN_PARAM = 'cpamp_bridge_parent_origin';

const requestIDPattern = /^[A-Za-z0-9_-]{1,96}$/;
const durationPattern = /^(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+$/u;
const maxRPM = 2_147_483_647;
const maxPendingRequests = 2_147_483_647;
const defaultMaxQueueWait = '35s';
const defaultMaxPendingRequests = 500;

export interface RateLimitBridgeSettings {
  enabled: boolean;
  rpm: number;
  minInterval: string;
  maxQueueWait: string;
  maxPendingRequests: number;
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
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 && value <= maxPendingRequests;

const isValidDuration = (value: unknown): value is string =>
  typeof value === 'string' && value.length <= 64 && durationPattern.test(value.trim());

const parseSettings = (value: unknown): RateLimitBridgeSettings | null => {
  if (!isRecord(value)) return null;
  const minInterval = typeof value.minInterval === 'string' ? value.minInterval.trim() : '';
  const maxQueueWait = typeof value.maxQueueWait === 'string' ? value.maxQueueWait.trim() : '';
  if (
    typeof value.enabled !== 'boolean' ||
    !isValidRPM(value.rpm) ||
    !isValidDuration(minInterval) ||
    !isValidDuration(maxQueueWait) ||
    !isValidPendingRequests(value.maxPendingRequests)
  ) {
    return null;
  }
  return {
    enabled: value.enabled,
    rpm: value.rpm,
    minInterval,
    maxQueueWait,
    maxPendingRequests: value.maxPendingRequests,
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
  const settings = parseSettings(value.settings);
  return settings
    ? {
        protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
        pluginID: RATE_LIMIT_BRIDGE_PLUGIN_ID,
        requestID: value.requestID,
        operation: 'save',
        settings,
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
): PluginConfigObject => ({
  enabled: request.settings.enabled,
  default: {
    rpm: request.settings.rpm,
    min_interval: request.settings.minInterval,
  },
  max_queue_wait: request.settings.maxQueueWait,
  max_pending_requests: request.settings.maxPendingRequests,
  accounts: null,
});

export const normalizeRateLimitBridgeSettings = (
  config: PluginConfigObject
): RateLimitBridgeSettings => {
  const defaultPolicy = isRecord(config.default) ? config.default : {};
  const rpm = isValidRPM(defaultPolicy.rpm) ? defaultPolicy.rpm : 0;
  const candidateInterval =
    typeof defaultPolicy.min_interval === 'string' ? defaultPolicy.min_interval.trim() : '';
  const candidateQueueWait =
    typeof config.max_queue_wait === 'string' ? config.max_queue_wait.trim() : '';

  return {
    enabled: config.enabled !== false,
    rpm,
    minInterval: isValidDuration(candidateInterval) ? candidateInterval : '0s',
    maxQueueWait: isValidDuration(candidateQueueWait) ? candidateQueueWait : defaultMaxQueueWait,
    maxPendingRequests: isValidPendingRequests(config.max_pending_requests)
      ? config.max_pending_requests
      : defaultMaxPendingRequests,
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
  (options: RateLimitBridgeHostOptions) =>
  async (event: RateLimitBridgeHostEvent): Promise<void> => {
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

      await options.patchConfig(buildRateLimitConfigPatch(request));
      options.reply(event.source, createRateLimitBridgeSuccess(request, request.settings));
    } catch {
      const error = request.operation === 'load' ? '无法读取限流设置' : '无法保存限流设置';
      options.reply(event.source, createRateLimitBridgeFailure(request, error));
    }
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
