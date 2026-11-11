import { describe, expect, it } from 'vitest';
import type {
  AccountBannedAuthFileDeleteResponse,
  CredentialRuntimeMetadataItem,
  TokenRecoveryBatchItem,
  TokenRecoveryBatchTargetRequest,
  TokenRecoveryTask,
} from '@/services/api/usageService';
import {
  buildPendingInspectionPageRecoveryOperation,
  chunkInspectionSnapshotTargets,
  mergeInspectionPageDeleteOperationResults,
  planInspectionSnapshotAccountBannedDelete,
  planInspectionSnapshotRecovery,
  reconcileInspectionPageDeleteResponse,
  selectPostInspectionManualSuccessKeys,
  summarizeInspectionPageRecoveryOperation,
  toInspectionPageDeleteOperationResult,
  type InspectionSnapshotTarget,
} from './inspectionPageBatchOperation';

const target = (clientKey: string): TokenRecoveryBatchTargetRequest => ({
  clientKey,
  fileName: `${clientKey}.json`,
  accountEmail: `${clientKey}@example.com`,
  provider: 'codex',
});

const metadata = (
  clientKey: string,
  status: string,
  options: { code?: string; message?: string } = {}
): CredentialRuntimeMetadataItem => ({
  clientKey,
  recoveryTask: {
    id: 1,
    fileName: `${clientKey}.json`,
    provider: 'codex',
    status,
    mode: 'manual',
    ...(options.code ? { lastErrorCode: options.code } : {}),
    ...(options.message ? { lastErrorMessage: options.message } : {}),
  },
});

const snapshotTarget = (
  clientKey: string,
  fileName = `${clientKey}.json`
): InspectionSnapshotTarget => ({
  recoveryTarget: {
    ...target(clientKey),
    fileName,
    authIndex: `index-${clientKey}`,
  },
  deletionSource: {
    clientKey,
    fileName,
    authIndex: `index-${clientKey}`,
    accountEmail: `${clientKey}@example.com`,
    provider: 'codex',
    runtimeId: `runtime-${clientKey}`,
    accountSnapshot: `${clientKey}@example.com`,
  },
});

const recoveryTask = (
  clientKey: string,
  overrides: Partial<TokenRecoveryTask> = {}
): TokenRecoveryTask => ({
  id: 1,
  fileName: `${clientKey}.json`,
  authIndex: `index-${clientKey}`,
  accountEmail: `${clientKey}@example.com`,
  provider: 'codex',
  status: 'manual_failed_manual_only',
  mode: 'manual',
  ...overrides,
});

const recoveryItem = (
  clientKey: string,
  task: TokenRecoveryTask | null
): TokenRecoveryBatchItem => ({
  clientKey,
  task,
});

describe('inspection page batch operation model', () => {
  it('keeps a submitted recovery operation active while its durable task is queued or running', () => {
    const operation = {
      pageSignature: 'reauth-page-1',
      targets: [target('queued'), target('running'), target('done')],
      submissionFailures: [],
      submissionComplete: true,
    };
    const items = new Map<string, CredentialRuntimeMetadataItem>([
      ['queued', metadata('queued', 'manual_queued')],
      ['running', metadata('running', 'manual_running')],
      ['done', metadata('done', 'succeeded')],
    ]);

    expect(summarizeInspectionPageRecoveryOperation(operation, items)).toEqual({
      active: true,
      submitting: false,
      queued: 1,
      running: 1,
      succeeded: 1,
      failed: 0,
      failureReasons: [],
    });
  });

  it('surfaces terminal recovery failures and unlocks once no recovery task is pending', () => {
    const operation = {
      pageSignature: 'reauth-page-1',
      targets: [target('failed'), target('rejected')],
      submissionFailures: [{ clientKey: 'rejected', reason: 'credential_not_found' }],
      submissionComplete: true,
    };
    const items = new Map<string, CredentialRuntimeMetadataItem>([
      ['failed', metadata('failed', 'manual_failed_manual_only', { message: 'upstream_rejected' })],
    ]);

    expect(summarizeInspectionPageRecoveryOperation(operation, items)).toEqual({
      active: false,
      submitting: false,
      queued: 0,
      running: 0,
      succeeded: 0,
      failed: 2,
      failureReasons: ['upstream_rejected', 'credential_not_found'],
    });
  });

  it('resumes the visible-page lock after a refresh when a durable recovery task is still pending', () => {
    const items = new Map<string, CredentialRuntimeMetadataItem>([
      ['pending', metadata('pending', 'manual_running')],
      ['finished', metadata('finished', 'succeeded')],
    ]);

    expect(
      buildPendingInspectionPageRecoveryOperation(
        'reauth-page-2',
        [target('pending'), target('finished')],
        items
      )
    ).toEqual({
      pageSignature: 'reauth-page-2',
      targets: [target('pending')],
      submissionFailures: [],
      submissionComplete: true,
    });
  });

  it('preserves bounded per-file delete results so skipped or failed cards are explainable', () => {
    const response: AccountBannedAuthFileDeleteResponse = {
      requested: 4,
      deleted: [{ clientKey: 'deleted', fileName: 'deleted.json' }],
      skipped: [{ clientKey: 'shared', fileName: 'shared.json', code: 'shared_file_incomplete' }],
      failed: [{ clientKey: 'failed', fileName: 'failed.json', message: 'cpa_timeout' }],
    };

    expect(toInspectionPageDeleteOperationResult(response)).toEqual({
      requested: 4,
      deleted: 1,
      alreadyAbsent: 0,
      skipped: 1,
      failed: 1,
      details: [
        { fileName: 'failed.json', code: 'cpa_timeout' },
        { fileName: 'shared.json', code: 'shared_file_incomplete' },
      ],
    });
  });

  it('selects only manual recoveries that completed after the inspection finished', () => {
    const items = new Map<string, CredentialRuntimeMetadataItem>([
      [
        'fresh',
        {
          ...metadata('fresh', 'succeeded'),
          recoveryTask: {
            ...metadata('fresh', 'succeeded').recoveryTask!,
            mode: 'manual',
            completedAtMs: 1_001,
          },
        },
      ],
      [
        'stale',
        {
          ...metadata('stale', 'succeeded'),
          recoveryTask: {
            ...metadata('stale', 'succeeded').recoveryTask!,
            mode: 'manual',
            completedAtMs: 1_000,
          },
        },
      ],
      [
        'auto',
        {
          ...metadata('auto', 'succeeded'),
          recoveryTask: {
            ...metadata('auto', 'succeeded').recoveryTask!,
            mode: 'auto',
            completedAtMs: 1_002,
          },
        },
      ],
    ]);

    expect(selectPostInspectionManualSuccessKeys(1_000, items)).toEqual(['fresh']);
  });

  it('reconciles only CPA-absent delete outcomes out of the inspection source set', () => {
    const response: AccountBannedAuthFileDeleteResponse = {
      requested: 4,
      deleted: [
        {
          clientKey: 'deleted',
          fileName: 'deleted-fallback.json',
          physicalName: 'deleted.json',
        },
      ],
      skipped: [
        {
          clientKey: 'missing',
          fileName: 'missing-fallback.json',
          physicalName: 'missing.json',
          code: 'credential_not_found',
        },
        {
          clientKey: 'kept',
          fileName: 'kept.json',
          code: 'shared_file_incomplete',
        },
      ],
      failed: [{ clientKey: 'failed', fileName: 'failed.json', code: 'delete_failed' }],
    };

    const reconciliation = reconcileInspectionPageDeleteResponse(response);

    expect(reconciliation.physicalNames).toEqual(['deleted.json', 'missing.json']);
    expect(reconciliation.alreadyAbsent).toBe(1);
    expect(reconciliation.result).toMatchObject({
      requested: 4,
      deleted: 1,
      alreadyAbsent: 1,
      skipped: 1,
      failed: 1,
    });
    expect(reconciliation.result.details).toEqual([
      { fileName: 'failed.json', code: 'delete_failed' },
      { fileName: 'kept.json', code: 'shared_file_incomplete' },
    ]);
  });

  it('chunks frozen inspection snapshot targets into bounded 100-item batches', () => {
    const chunks = chunkInspectionSnapshotTargets(Array.from({ length: 201 }, (_, index) => index));

    expect(chunks.map((chunk) => chunk.length)).toEqual([100, 100, 1]);
    expect(chunks[0][0]).toBe(0);
    expect(chunks[2]).toEqual([200]);
  });

  it('submits only recovery candidates that are not pending, newly succeeded, banned, or unavailable', () => {
    const candidates = [
      snapshotTarget('new'),
      snapshotTarget('failed'),
      snapshotTarget('queued'),
      snapshotTarget('done'),
      snapshotTarget('banned'),
      snapshotTarget('query-error'),
      snapshotTarget('missing'),
    ];
    const plan = planInspectionSnapshotRecovery({
      candidates,
      inspectionFinishedAtMs: 10_000,
      taskItemsByClientKey: new Map<string, TokenRecoveryBatchItem>([
        ['new', recoveryItem('new', null)],
        ['failed', recoveryItem('failed', recoveryTask('failed', { lastErrorCode: 'http_401' }))],
        ['queued', recoveryItem('queued', recoveryTask('queued', { status: 'manual_queued' }))],
        [
          'done',
          recoveryItem(
            'done',
            recoveryTask('done', { status: 'succeeded', completedAtMs: 10_001 })
          ),
        ],
        [
          'banned',
          recoveryItem(
            'banned',
            recoveryTask('banned', {
              lastErrorCode: 'account_banned',
            })
          ),
        ],
        [
          'query-error',
          {
            clientKey: 'query-error',
            task: null,
            errorCode: 'recovery_unavailable',
          },
        ],
      ]),
    });

    expect(plan.submitTargets.map((item) => item.clientKey)).toEqual(['new', 'failed']);
    expect(plan.skipped).toEqual([
      { clientKey: 'queued', reason: 'recovery_pending' },
      { clientKey: 'done', reason: 'recovery_succeeded' },
      { clientKey: 'banned', reason: 'account_banned' },
      { clientKey: 'query-error', reason: 'recovery_unavailable' },
      { clientKey: 'missing', reason: 'recovery_task_unavailable' },
    ]);
  });

  it('keeps an entire shared physical file out of banned deletion when any frozen member is not banned', () => {
    const candidates = [
      snapshotTarget('shared-banned', 'shared.json'),
      snapshotTarget('shared-not-banned', 'shared.json'),
    ];
    const plan = planInspectionSnapshotAccountBannedDelete({
      candidates,
      taskItemsByClientKey: new Map<string, TokenRecoveryBatchItem>([
        [
          'shared-banned',
          recoveryItem(
            'shared-banned',
            recoveryTask('shared-banned', {
              fileName: 'shared.json',
              lastErrorCode: 'account_banned',
            })
          ),
        ],
        [
          'shared-not-banned',
          recoveryItem(
            'shared-not-banned',
            recoveryTask('shared-not-banned', {
              fileName: 'shared.json',
              lastErrorCode: 'http_401',
            })
          ),
        ],
      ]),
    });

    expect(plan.batches).toEqual([]);
    expect(plan.targetCount).toBe(0);
    expect(plan.fileCount).toBe(0);
    expect(plan.skipped).toEqual([
      { clientKey: 'shared-banned', reason: 'shared_file_incomplete' },
      { clientKey: 'shared-not-banned', reason: 'shared_file_incomplete' },
    ]);
  });

  it('does not split shared files across snapshot banned-delete batches', () => {
    const oversized = Array.from({ length: 101 }, (_, index) =>
      snapshotTarget(`oversized-${index}`, 'oversized.json')
    );
    const ninetyNine = Array.from({ length: 99 }, (_, index) =>
      snapshotTarget(`ninety-nine-${index}`, 'ninety-nine.json')
    );
    const two = [snapshotTarget('two-a', 'two.json'), snapshotTarget('two-b', 'two.json')];
    const candidates = [...oversized, ...ninetyNine, ...two];
    const taskItemsByClientKey = new Map<string, TokenRecoveryBatchItem>(
      candidates.map((candidate) => {
        const clientKey = candidate.recoveryTarget.clientKey;
        return [
          clientKey,
          recoveryItem(
            clientKey,
            recoveryTask(clientKey, {
              fileName: candidate.recoveryTarget.fileName,
              lastErrorCode: 'account_banned',
            })
          ),
        ];
      })
    );

    const plan = planInspectionSnapshotAccountBannedDelete({ candidates, taskItemsByClientKey });

    expect(plan.batches.map((batch) => batch.length)).toEqual([99, 2]);
    expect(plan.batches[0].every((item) => item.fileName === 'ninety-nine.json')).toBe(true);
    expect(plan.batches[1].every((item) => item.fileName === 'two.json')).toBe(true);
    expect(plan.skipped).toHaveLength(101);
    expect(plan.skipped.every((item) => item.reason === 'shared_file_too_large')).toBe(true);
  });

  it('accumulates bounded deletion results across serial snapshot batches', () => {
    const first: AccountBannedAuthFileDeleteResponse = {
      requested: 2,
      deleted: [{ clientKey: 'one', fileName: 'one.json' }],
      skipped: [{ clientKey: 'two', fileName: 'two.json', code: 'credential_not_found' }],
      failed: [],
    };
    const second: AccountBannedAuthFileDeleteResponse = {
      requested: 2,
      deleted: [],
      skipped: [{ clientKey: 'three', fileName: 'three.json', code: 'shared_file_incomplete' }],
      failed: [{ clientKey: 'four', fileName: 'four.json', code: 'delete_failed' }],
    };

    expect(
      mergeInspectionPageDeleteOperationResults(
        mergeInspectionPageDeleteOperationResults(
          null,
          toInspectionPageDeleteOperationResult(first)
        ),
        toInspectionPageDeleteOperationResult(second)
      )
    ).toEqual({
      requested: 4,
      deleted: 1,
      alreadyAbsent: 1,
      skipped: 1,
      failed: 1,
      details: [
        { fileName: 'four.json', code: 'delete_failed' },
        { fileName: 'three.json', code: 'shared_file_incomplete' },
      ],
    });
  });
});
