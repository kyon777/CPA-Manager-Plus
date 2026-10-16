# 300-account Batch Auth Upload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Allow one user operation to import up to 300 authentication JSON accounts, upload them through an 8-slot Manager-side worker pool, split oversized multipart requests automatically, and perform only one credential-list refresh after the logical operation.

**Architecture:** Keep the existing asynchronous /v0/management/auth-files/batch-upload job protocol. Raise server limits to 300 files and 64 MiB, and add a service-wide eight-slot semaphore so concurrent jobs share one global limit. In the web API, partition a logical upload by file count and a 48 MiB safe payload threshold, aggregate child-job results, and let the existing hook reload the list once after aggregation. No quota, inspection, reauthorization, or usage-database call is added.

**Tech Stack:** Go manager-server, React/TypeScript, Axios wrapper, Vitest, Go testing, existing CPAM release scripts.

---

### Task 1: Server limits and global eight-slot concurrency

**Files:**
- Modify: apps/manager-server/internal/service/authfilesbatch/service.go
- Modify: apps/manager-server/internal/service/authfilesbatch/service_test.go
- Modify: apps/manager-server/internal/http/controller/authfilesbatch/handler.go

- [x] **Step 1: Add failing tests first.**

Extend service_test.go with a counting client and tests named TestBatchUploadAcceptsUpTo300Files, TestBatchUploadRejects301Files, and TestBatchUploadUsesAtMostEightGlobalUploadSlots. The first creates 300 unique UploadFile values and waits for Status.Finished; the second creates 301 and asserts the error contains 300; the third blocks 16 uploads, records maximum active calls, releases them, and asserts the maximum is no greater than the exported concurrency constant. Keep the existing inventory invalidation test and assert exactly one invalidation for a successful or partial job.

- [x] **Step 2: Verify the new tests fail against the current implementation.**

Run:

~~~powershell
go test ./internal/service/authfilesbatch -run "TestBatchUpload(AcceptsUpTo300Files|Rejects301Files|UsesAtMostEightGlobalUploadSlots)" -count=1
~~~

Expected: the 301-file test reports the old 100-file limit and the concurrency test cannot observe the requested eight-slot implementation.

- [x] **Step 3: Implement the minimal server change.**

Change the service constants to MaxFiles = 300, MaxTotalSize = 64 * 1024 * 1024, and MaxUploadConcurrency = 8. Add uploadSlots chan struct{} to Service, initialize it in NewWithInventoryInvalidator, and replace the serial run loop with a worker pool. Workers consume pending files, acquire one global slot before calling CPA Core, release it after the call, zero the byte buffer, and call record exactly once. Resolve CPA setup once per job. Preserve safe error sanitization, one-time inventory invalidation, and existing job status fields. The HTTP handler must continue deriving its request limit from MaxTotalSize.

- [x] **Step 4: Run focused and package-wide tests.**

~~~powershell
go test ./internal/service/authfilesbatch -run "TestBatchUpload" -count=1
go test ./internal/service/authfilesbatch -count=1
~~~

Update the old serial-order assertion to compare names as a set because worker completion order is nondeterministic; verify no credential contents appear in status.

- [x] **Step 5: Commit the server change.**

~~~powershell
git add apps/manager-server/internal/service/authfilesbatch/service.go apps/manager-server/internal/service/authfilesbatch/service_test.go apps/manager-server/internal/http/controller/authfilesbatch/handler.go
git commit -m "feat: parallelize auth file batch uploads"
~~~

### Task 2: Web API partitioning and result aggregation

**Files:**
- Modify: apps/web/src/services/api/authFiles.ts
- Modify: apps/web/src/services/api/authFiles.test.ts

- [x] **Step 1: Add failing API tests.**

Add a test that creates 300 small File objects, mocks one completed job response, and asserts one batch-upload POST plus 300 successful names. Add a second test with two files whose combined size exceeds the safe threshold; mock one successful and one partial child job and assert two POSTs and an aggregated partial result containing the failed filename and reason.

- [x] **Step 2: Verify the tests fail before implementation.**

~~~powershell
npm --workspace apps/web run test -- src/services/api/authFiles.test.ts -t "300 files|safe request size"
~~~

- [x] **Step 3: Implement deterministic partitioning.**

Export MAX_AUTH_FILE_BATCH_FILES = 300 and MAX_AUTH_FILE_BATCH_BYTES = 48 * 1024 * 1024. Add partitionAuthFileBatch(files), which greedily starts a new chunk before either limit is exceeded. Reject more than 300 files before any network request. Keep a single oversized file in its own chunk so the server can return its normal per-file error. Refactor the current one-job implementation into a chunk function, then make uploadFiles submit chunks sequentially and combine uploaded, successful names, and normalized failures. Return ok, partial, or error using the aggregate result; never resubmit a successful chunk.

- [x] **Step 4: Run the API tests.**

~~~powershell
npm --workspace apps/web run test -- src/services/api/authFiles.test.ts -t "300 files|safe request size"
npm --workspace apps/web run test -- src/services/api/authFiles.test.ts
~~~

- [x] **Step 5: Commit the API change.**

~~~powershell
git add apps/web/src/services/api/authFiles.ts apps/web/src/services/api/authFiles.test.ts
git commit -m "feat: split oversized auth upload batches"
~~~

### Task 3: Paste/file guardrails and one final list refresh

**Files:**
- Modify: apps/web/src/features/authFiles/sessionAuthConverter.ts
- Modify: apps/web/src/features/authFiles/hooks/useAuthFilesData.ts
- Modify: apps/web/src/features/authFiles/hooks/useAuthFilesData.test.ts
- Modify: apps/web/src/i18n/locales/zh-CN.json
- Modify: apps/web/src/i18n/locales/en.json

- [x] **Step 1: Add failing boundary tests.**

Add hook tests for a CPA array of exactly 300 records and another of 301. The 300 case must call the upload API once, show the existing multi-file success notification, and call the list loader once. The 301 case must show a translated maximum-count error, make no upload request, and make no list reload. Preserve the existing partial-upload failure-name test.

- [x] **Step 2: Verify the boundary tests fail.**

~~~powershell
npm --workspace apps/web run test -- src/features/authFiles/hooks/useAuthFilesData.test.ts -t "300|301|paste_success_many"
~~~

- [x] **Step 3: Implement limits without adding quota calls.**

Use a 48 MiB paste-specific parser limit (retain the lower-level converter's existing default for other callers), export a 300-record import constant, and reject more than 300 payloads after buildPastedAuthJsonPayloads and after prepareAuthFilesForUpload. Add localized paste_error_too_many and upload_error_too_many messages. Keep the API guard as defense in depth. Do not change reloadAfterMutation to run per file or per child chunk: the hook calls it at most once after the aggregate result. Do not add account-window-usage, quota, inspection, reauthorization, or usage-database calls.

- [x] **Step 4: Run web tests, type-check, and lint.**

~~~powershell
npm --workspace apps/web run test -- src/features/authFiles/hooks/useAuthFilesData.test.ts src/features/authFiles/sessionAuthConverter.test.ts
npm --workspace apps/web run type-check
npm --workspace apps/web run lint
~~~

- [x] **Step 5: Commit the guardrails.**

~~~powershell
git add apps/web/src/features/authFiles/sessionAuthConverter.ts apps/web/src/features/authFiles/hooks/useAuthFilesData.ts apps/web/src/features/authFiles/hooks/useAuthFilesData.test.ts apps/web/src/i18n/locales/zh-CN.json apps/web/src/i18n/locales/en.json
git commit -m "feat: cap auth imports at 300 accounts"
~~~

### Task 4: Full verification and CPAM-only release check

**Files:**
- Modify only if required by existing CPAM build metadata.

- [x] **Step 1: Run complete tests and builds.**

~~~powershell
go test ./...
npm test
npm run build
go build ./cmd/cpa-manager-plus
~~~

- [x] **Step 2: Inspect the final diff for scope and accounting safety.**

~~~powershell
git diff a85e700094f5340c2e0e91eed881415f79755cb --stat
git diff a85e700094f5340c2e0e91eed881415f79755cb -- apps/manager-server apps/web/src/features/authFiles apps/web/src/services/api/authFiles.ts apps/web/src/i18n/locales
~~~

Confirm no CPA Core, cli-proxy-api, usage.sqlite, quota-fetch, inspection, or reauthorization code changed.

- [x] **Step 3: Commit the verified implementation.**

~~~powershell
git status --short
git commit -am "feat: support fast 300-account auth imports"
~~~

Deployment remains a separate CPAM-only operation: rebuild only cpa-manager-plus, verify the CPA Core container identity is unchanged, and push only origin/lo/custom according to CPAM_UPDATE_RUNBOOK.md.
