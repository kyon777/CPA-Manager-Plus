/**
 * Sends a response to an iframe source which was already authenticated at message
 * ingress by the corresponding bridge host.  Do not call this before the strict
 * source/origin validation in `parseTrusted*BridgeRequest` has completed.
 */
export const replyToTrustedPluginFrame = (
  source: MessageEventSource | null,
  targetOrigin: string,
  response: unknown
): boolean => {
  if (!source || !targetOrigin) return false;

  try {
    // A trusted iframe event source is the WindowProxy for that frame.  Replying
    // directly avoids losing an in-flight response when React replaces the ref.
    (source as WindowProxy).postMessage(response, targetOrigin);
    return true;
  } catch {
    return false;
  }
};
