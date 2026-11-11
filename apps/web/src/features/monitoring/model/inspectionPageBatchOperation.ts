import type {
  AccountBannedAuthFileDeleteResponse,
  AccountBannedAuthFileDeleteTargetRequest,
  CredentialRuntimeMetadataItem,
  TokenRecoveryBatchItem,
  TokenRecoveryBatchTargetRequest,
} from '@/services/api/usageService';
import {
  buildAccountBannedDeleteTarget,
  isTerminalAccountBannedRecoveryTask,
  type AccountBannedDeleteSource,
} from '@/features/authFiles/model/accountBannedDelete';
import { isPendingRecoveryStatus } from '@/features/accounts/model/credentialRuntimeMetadata';

export const INSPECTION_SNAPSHOT_BATCH_LIMIT = 100;

export type InspectionSnapshotTarget = {
  recoveryTarget: TokenRecoveryBatchTargetRequest;
  deletionSource: AccountBannedDeleteSource;
};

export type InspectionSnapshotSkip = {
  clientKey: string;
  reason: string;
};

export type InspectionSnapshotRecoveryPlan = {
  submitTargets: readonly TokenRecoveryBatchTargetRequest[];
  skipped: readonly InspectionSnapshotSkip[];
};

export type InspectionSnapshotBannedDeletePlan = {
  batches: readonly (readonly AccountBannedAuthFileDeleteTargetRequest[])[];
  skipped: readonly InspectionSnapshotSkip[];
  targetCount: number;
  fileCount: number;
};

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
  alreadyAbsent: number;
  skipped: number;
  failed: number;
  details: readonly InspectionPageDeleteOperationDetail[];
};

export type InspectionPageDeleteReconciliation = {
  physicalNames: readonly string[];
  alreadyAbsent: number;
  result: InspectionPageDeleteOperationResult;
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

export const chunkInspectionSnapshotTargets = <T>(
  targets: readonly T[],
  limit = INSPECTION_SNAPSHOT_BATCH_LIMIT
): T[][] => {
  if (!Number.isSafeInteger(limit) || limit <= 0) {
    throw new Error('inspection snapshot batch limit must be a positive integer');
  }

  const chunks: T[][] = [];
  for (let offset = 0; offset < targets.length; offset += limit) {
    chunks.push(targets.slice(offset, offset + limit));
  }
  return chunks;
};

export const planInspectionSnapshotRecovery = ({
  candidates,
  taskItemsByClientKey,
  inspectionFinishedAtMs,
}: {
  candidates: readonly InspectionSnapshotTarget[];
  taskItemsByClientKey: ReadonlyMap<string, TokenRecoveryBatchItem>;
  inspectionFinishedAtMs: number;
}): InspectionSnapshotRecoveryPlan => {
  const submitTargets: TokenRecoveryBatchTargetRequest[] = [];
  const skipped: InspectionSnapshotSkip[] = [];

  candidates.forEach(({ recoveryTarget }) => {
    const clientKey = normalizedText(recoveryTarget.clientKey);
    const item = taskItemsByClientKey.get(clientKey);
    if (!item) {
      skipped.push({ clientKey, reason: 'recovery_task_unavailable' });
      return;
    }

    const task = item.task;
    if (!task) {
      submitTargets.push(recoveryTarget);
      return;
    }
    if (isPendingRecoveryStatus(task.status)) {
      skipped.push({ clientKey, reason: 'recovery_pending' });
      return;
    }
    if (isTerminalAccountBannedRecoveryTask(task)) {
      skipped.push({ clientKey, reason: 'account_banned' });
      return;
    }
    if (
      task.status === 'succeeded' &&
      Number.isFinite(inspectionFinishedAtMs) &&
      Number.isFinite(task.completedAtMs) &&
      (task.completedAtMs ?? 0) > inspectionFinishedAtMs
    ) {
      skipped.push({ clientKey, reason: 'recovery_succeeded' });
      return;
    }

    submitTargets.push(recoveryTarget);
  });

  return { submitTargets, skipped };
};

const snapshotPhysicalFileKey = (source: AccountBannedDeleteSource, index: number): string => {
  const fileName = normalizedText(source.fileName).toLowerCase();
  return fileName || `__missing_file_name_${index}`;
};

export const planInspectionSnapshotAccountBannedDelete = ({
  candidates,
  taskItemsByClientKey,
}: {
  candidates: readonly InspectionSnapshotTarget[];
  taskItemsByClientKey: ReadonlyMap<string, TokenRecoveryBatchItem>;
}): InspectionSnapshotBannedDeletePlan => {
  const candidateGroups = new Map<string, InspectionSnapshotTarget[]>();
  candidates.forEach((candidate, index) => {
    const physicalKey = snapshotPhysicalFileKey(candidate.deletionSource, index);
    const group = candidateGroups.get(physicalKey);
    if (group) group.push(candidate);
    else candidateGroups.set(physicalKey, [candidate]);
  });

  const completeGroups: AccountBannedAuthFileDeleteTargetRequest[][] = [];
  const skipped: InspectionSnapshotSkip[] = [];
  candidateGroups.forEach((group) => {
    const targets = group.map(({ deletionSource, recoveryTarget }) =>
      buildAccountBannedDeleteTarget(
        deletionSource,
        taskItemsByClientKey.get(normalizedText(recoveryTarget.clientKey))?.task
      )
    );
    if (targets.some((target) => !target)) {
      group.forEach(({ recoveryTarget }) => {
        skipped.push({
          clientKey: normalizedText(recoveryTarget.clientKey),
          reason: 'shared_file_incomplete',
        });
      });
      return;
    }
    const strictTargets = targets as AccountBannedAuthFileDeleteTargetRequest[];
    if (strictTargets.length > INSPECTION_SNAPSHOT_BATCH_LIMIT) {
      strictTargets.forEach((target) => {
        skipped.push({ clientKey: target.clientKey, reason: 'shared_file_too_large' });
      });
      return;
    }
    completeGroups.push(strictTargets);
  });

  const batches: AccountBannedAuthFileDeleteTargetRequest[][] = [];
  let currentBatch: AccountBannedAuthFileDeleteTargetRequest[] = [];
  completeGroups.forEach((group) => {
    if (currentBatch.length > 0 && currentBatch.length + group.length > INSPECTION_SNAPSHOT_BATCH_LIMIT) {
      batches.push(currentBatch);
      currentBatch = [];
    }
    currentBatch.push(...group);
  });
  if (currentBatch.length > 0) batches.push(currentBatch);

  return {
    batches,
    skipped,
    targetCount: completeGroups.reduce((total, group) => total + group.length, 0),
    fileCount: completeGroups.length,
  };
};

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

const physicalNameOf = (item: { fileName: string; physicalName?: string }): string =>
  normalizedText(item.physicalName) || normalizedText(item.fileName);

const uniquePhysicalNames = (
  items: readonly { fileName: string; physicalName?: string }[]
): string[] => {
  const names = new Set<string>();
  items.forEach((item) => {
    const physicalName = physicalNameOf(item);
    if (physicalName) names.add(physicalName);
  });
  return Array.from(names);
};

export const reconcileInspectionPageDeleteResponse = (
  response: AccountBannedAuthFileDeleteResponse
): InspectionPageDeleteReconciliation => {
  const alreadyAbsentItems = response.skipped.filter(
    (item) => normalizedText(item.code) === 'credential_not_found'
  );
  const retainedSkippedItems = response.skipped.filter(
    (item) => normalizedText(item.code) !== 'credential_not_found'
  );
  const alreadyAbsent = alreadyAbsentItems.length;

  return {
    physicalNames: uniquePhysicalNames([...response.deleted, ...alreadyAbsentItems]),
    alreadyAbsent,
    result: {
      requested: response.requested,
      deleted: response.deleted.length,
      alreadyAbsent,
      skipped: retainedSkippedItems.length,
      failed: response.failed.length,
      details: [...response.failed, ...retainedSkippedItems]
        .slice(0, maxDeleteDetails)
        .map(toDeleteDetail),
    },
  };
};

export const toInspectionPageDeleteOperationResult = (
  response: AccountBannedAuthFileDeleteResponse
): InspectionPageDeleteOperationResult => reconcileInspectionPageDeleteResponse(response).result;

export const mergeInspectionPageDeleteOperationResults = (
  previous: InspectionPageDeleteOperationResult | null,
  next: InspectionPageDeleteOperationResult
): InspectionPageDeleteOperationResult => ({
  requested: (previous?.requested ?? 0) + next.requested,
  deleted: (previous?.deleted ?? 0) + next.deleted,
  alreadyAbsent: (previous?.alreadyAbsent ?? 0) + next.alreadyAbsent,
  skipped: (previous?.skipped ?? 0) + next.skipped,
  failed: (previous?.failed ?? 0) + next.failed,
  details: [...(previous?.details ?? []), ...next.details].slice(0, maxDeleteDetails),
});
