import { describe, expect, it } from 'vitest';
import type {
  AccountBannedAuthFileDeleteResponse,
  CredentialRuntimeMetadataItem,
  TokenRecoveryBatchTargetRequest,
} from '@/services/api/usageService';
import {
  buildPendingInspectionPageRecoveryOperation,
  summarizeInspectionPageRecoveryOperation,
  toInspectionPageDeleteOperationResult,
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
      submissionFailures: [
        { clientKey: 'rejected', reason: 'credential_not_found' },
      ],
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
      buildPendingInspectionPageRecoveryOperation('reauth-page-2', [target('pending'), target('finished')], items)
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
      skipped: 1,
      failed: 1,
      details: [
        { fileName: 'failed.json', code: 'cpa_timeout' },
        { fileName: 'shared.json', code: 'shared_file_incomplete' },
      ],
    });
  });
});
