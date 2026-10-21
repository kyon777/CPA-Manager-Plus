import type {
  AccountBannedAuthFileDeleteResponse,
  CredentialRuntimeMetadataItem,
  TokenRecoveryBatchTargetRequest,
} from '@/services/api/usageService';
import { isPendingRecoveryStatus } from '@/features/accounts/model/credentialRuntimeMetadata';

export type InspectionPageRecoverySubmissionFailure = {
  clientKey: string;
  reason: string;
};

export type InspectionPageRecoveryOperation = {
  pageSignature: string;
  targets: readonly TokenRecoveryBatchTargetRequest[];
  submissionFailures: readonly InspectionPageRecoverySubmissionFailure[];
  submissionComplete: boolean;
};

export type InspectionPageRecoveryProgress = {
  active: boolean;
  submitting: boolean;
  queued: number;
  running: number;
  succeeded: number;
  failed: number;
  failureReasons: readonly string[];
};

export type InspectionPageDeletePhase =
  | 'idle'
  | 'awaiting_first_confirmation'
  | 'awaiting_second_confirmation'
  | 'deleting'
  | 'completed';

export type InspectionPageDeleteOperationDetail = {
  fileName: string;
  code: string;
};

export type InspectionPageDeleteOperationResult = {
  requested: number;
  deleted: number;
  skipped: number;
  failed: number;
  details: readonly InspectionPageDeleteOperationDetail[];
};

export type InspectionPageDeleteOperation = {
  phase: InspectionPageDeletePhase;
  result: InspectionPageDeleteOperationResult | null;
  error: string;
};

export const idleInspectionPageDeleteOperation: InspectionPageDeleteOperation = {
  phase: 'idle',
  result: null,
  error: '',
};

const maxFailureReasons = 6;
const maxDeleteDetails = 8;

const normalizedText = (value: string | null | undefined): string => value?.trim() ?? '';

const appendUniqueBounded = (items: string[], value: string): void => {
  const normalized = value.trim();
  if (!normalized || items.length >= maxFailureReasons || items.includes(normalized)) return;
  items.push(normalized);
};

const taskFailureReason = (item: CredentialRuntimeMetadataItem | undefined): string => {
  const task = item?.recoveryTask;
  return (
    normalizedText(task?.lastErrorMessage) ||
    normalizedText(task?.lastErrorCode) ||
    normalizedText(item?.errorCode) ||
    normalizedText(task?.status) ||
    'recovery_task_unavailable'
  );
};

export const summarizeInspectionPageRecoveryOperation = (
  operation: InspectionPageRecoveryOperation | null,
  itemsByClientKey: ReadonlyMap<string, CredentialRuntimeMetadataItem>
): InspectionPageRecoveryProgress | null => {
  if (!operation) return null;
  if (!operation.submissionComplete) {
    return {
      active: true,
      submitting: true,
      queued: 0,
      running: 0,
      succeeded: 0,
      failed: 0,
      failureReasons: [],
    };
  }

  const failuresByClientKey = new Map(
    operation.submissionFailures.map((failure) => [failure.clientKey, failure.reason])
  );
  const failureReasons: string[] = [];
  let queued = 0;
  let running = 0;
  let succeeded = 0;
  let failed = 0;

  operation.targets.forEach((target) => {
    const item = itemsByClientKey.get(target.clientKey);
    const task = item?.recoveryTask;
    if (task && isPendingRecoveryStatus(task.status)) {
      if (task.status.endsWith('_queued')) queued += 1;
      else running += 1;
      return;
    }
    if (task?.status === 'succeeded') {
      succeeded += 1;
      return;
    }

    failed += 1;
    appendUniqueBounded(
      failureReasons,
      failuresByClientKey.get(target.clientKey) || taskFailureReason(item)
    );
  });

  return {
    active: queued > 0 || running > 0,
    submitting: false,
    queued,
    running,
    succeeded,
    failed,
    failureReasons,
  };
};

export const buildPendingInspectionPageRecoveryOperation = (
  pageSignature: string,
  targets: readonly TokenRecoveryBatchTargetRequest[],
  itemsByClientKey: ReadonlyMap<string, CredentialRuntimeMetadataItem>
): InspectionPageRecoveryOperation | null => {
  const pendingTargets = targets.filter((target) =>
    isPendingRecoveryStatus(itemsByClientKey.get(target.clientKey)?.recoveryTask?.status)
  );
  if (pendingTargets.length === 0) return null;
  return {
    pageSignature,
    targets: pendingTargets,
    submissionFailures: [],
    submissionComplete: true,
  };
};

export const selectPostInspectionManualSuccessKeys = (
  inspectionFinishedAtMs: number,
  itemsByClientKey: ReadonlyMap<string, CredentialRuntimeMetadataItem>
): string[] => {
  if (!Number.isFinite(inspectionFinishedAtMs)) return [];

  const keys: string[] = [];
  itemsByClientKey.forEach((item, clientKey) => {
    const task = item.recoveryTask;
    const completedAtMs = task?.completedAtMs;
    if (
      task?.mode !== 'manual' ||
      task.status !== 'succeeded' ||
      !Number.isFinite(completedAtMs) ||
      (completedAtMs ?? 0) <= inspectionFinishedAtMs
    ) {
      return;
    }

    const normalizedClientKey = normalizedText(clientKey);
    if (normalizedClientKey) keys.push(normalizedClientKey);
  });
  return keys;
};

const toDeleteDetail = (
  item: { fileName: string; code?: string; message?: string }
): InspectionPageDeleteOperationDetail => ({
  fileName: item.fileName.trim() || 'unknown.json',
  code: normalizedText(item.code) || normalizedText(item.message) || 'unknown_error',
});

export const toInspectionPageDeleteOperationResult = (
  response: AccountBannedAuthFileDeleteResponse
): InspectionPageDeleteOperationResult => ({
  requested: response.requested,
  deleted: response.deleted.length,
  skipped: response.skipped.length,
  failed: response.failed.length,
  details: [...response.failed, ...response.skipped].slice(0, maxDeleteDetails).map(toDeleteDetail),
});
