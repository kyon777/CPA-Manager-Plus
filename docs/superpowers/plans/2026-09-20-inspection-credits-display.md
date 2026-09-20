# Inspection Credits Display Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Show the Credits value captured by a Codex inspection beside the quota remaining percentage in both local and server inspection results.

**Architecture:** Carry the raw `credits` observation (`balance`, `has_credits`, `unlimited`, and presence) from the probe result to the shared React quota renderer. Server inspections additionally persist those fields in `codex_inspection_results`, so refreshing the server inspection page does not discard the displayed Credits value. Existing action decisions remain unchanged.

**Tech Stack:** React/TypeScript, Vitest, Go, SQLite migrations, Go tests, Docker Compose.

---

### Task 1: Define the visible Credits behavior with failing UI tests

**Files:**
- Modify: `apps/web/src/features/monitoring/components/CodexInspectionQuotaWindows.test.tsx`

- [ ] Add a render test that passes an observed Credits balance (`996.8907575`) and expects `Credits 996.8907575` in the quota header.
- [ ] Add a render test that passes an observed Credits object with `hasCredits: true` and no numeric balance and expects `Credits 可用`.
- [ ] Run the focused test before implementation; it must fail because the renderer has no Credits input or visible label.

### Task 2: Preserve Credits in local inspection results and render them

**Files:**
- Modify: `apps/web/src/features/monitoring/codexInspection.ts`
- Modify: `apps/web/src/features/monitoring/model/codexInspectionProbe.ts`
- Modify: `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx`
- Modify: `apps/web/src/features/monitoring/components/CodexInspectionQuotaWindows.tsx`
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.module.scss`

- [ ] Add optional inspection-result fields for Credits observation, balance, `has_credits`, and `unlimited`.
- [ ] Populate them only when the CPA response contains a `credits` object; preserve a zero balance as a real value.
- [ ] Pass those fields to the shared quota renderer and display the numeric balance first, then unlimited, then usable, then `--` for an observed-but-unvalued Credits object.
- [ ] Keep all existing quota bar, reset-time, and action-decision behavior intact.
- [ ] Run the focused UI test and confirm it passes.

### Task 3: Persist server inspection Credits across refreshes

**Files:**
- Modify: `apps/manager-server/internal/model/codex_inspection.go`
- Modify: `apps/manager-server/internal/service/codexinspection/service.go`
- Modify: `apps/manager-server/internal/repository/sqlite/migrate.go`
- Modify: `apps/manager-server/internal/repository/codexinspection/repository.go`
- Modify: `apps/manager-server/internal/repository/codexinspection/repository_test.go`

- [ ] Add nullable balance/flag fields plus an observed marker to the inspection result model.
- [ ] Parse the response `credits` map separately from the existing action-decision parser, without changing its decision logic.
- [ ] Store and retrieve the observation in SQLite; make the schema migration additive and safe for existing databases.
- [ ] Add a repository round-trip test proving a stored numeric balance and boolean flags are returned by `ListResults`.
- [ ] Run the focused repository/service Go tests and confirm they pass.

### Task 4: Map the server API result to the shared renderer

**Files:**
- Modify: `apps/web/src/services/api/usageService.ts`
- Modify: `apps/web/src/features/monitoring/ServerCodexInspectionPage.tsx`

- [ ] Extend the server API result type with the persisted Credits fields.
- [ ] Carry them through `toServerResultItem`, then reuse the Task 2 renderer path.
- [ ] Run the focused web component test and TypeScript check.

### Task 5: Verify, commit, deploy, and push only the custom branch

**Files:**
- Modify: `C:\kyon\docker\cpa-manager\compose.yaml` only for the custom image version/source commit metadata.

- [ ] Run focused web and Go tests, full web test suite, type-check, production web build, and `git diff --check`.
- [ ] Review the diff for scope and make sure no credentials are tracked.
- [ ] Commit to `lo/custom`, update Docker Compose metadata, build/restart only `cpa-manager-plus`, and poll `/health`.
- [ ] Push only `origin lo/custom`; never push `upstream`.
- [ ] Note that pre-deployment historical server inspection rows have no stored Credits observation and require a new server inspection to populate it.
