# A2A Tape Store Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement a durable, owner-isolated A2A 1.0 taskstore.Store on Tape, including direct-message persistence and four Godog acceptance scenarios.

**Architecture:** Add pkg/tape/a2a with a storage-profile record codec and TapeTaskStore backed by storage.TapeStorage. Each Task mutation is one CustomEntry containing the complete native A2A Task snapshot and its triggering native Event. Reads rebuild the projection from Tape; the official A2A server retains transport, lifecycle, cancellation, and streaming responsibilities.

**Tech Stack:** Go 1.26, github.com/a2aproject/a2a-go/v2 v2.3.1, existing JSONL/bbolt Tape storage, github.com/cucumber/godog v0.15.1, testing, httptest.

---

Apply @test-driven-development to every production behavior: write one focused test, run it and confirm the expected feature-missing failure, then add only enough code to pass. Run go test ./... before every commit.

### Task 1: Pin dependencies and add the native record codec

**Files:**

- Modify: go.mod
- Modify: go.sum
- Create: pkg/tape/a2a/record.go
- Create: pkg/tape/a2a/record_test.go

**Step 1: Pin dependencies**

Run:

    go get github.com/a2aproject/a2a-go/v2@v2.3.1
    go get github.com/cucumber/godog@v0.15.1

Expected: both exact versions appear in go.mod.

**Step 2: Write failing codec tests**

Add TestRecordRoundTrip and TestRecordRejectsMismatchedEventIdentity. The wished-for API is:

    task := testTask("task-1", "context-1", a2a.TaskStateWorking)
    event := a2a.NewStatusUpdateEvent(task, a2a.TaskStateWorking, nil)
    record, err := newTaskRecord("owner-a", task, event, 1)
    entry := record.entry()
    got, err := recordFromEntry(entry)

Assert native Task/Event round-trip equality, profile and A2A versions, searchable IDs, and rejection when Event task/context IDs do not match the Task.

**Step 3: Verify RED**

Run: go test ./pkg/tape/a2a -run TestRecord -v

Expected: FAIL because newTaskRecord and recordFromEntry are undefined.

**Step 4: Implement the minimal codec**

Define profile version 1, A2A version 1.0, extension key a2a:record, and entry kinds a2a:task, a2a:message, a2a:status_update, and a2a:artifact_update.

The record struct contains profileVersion, a2aVersion, recordId, owner, kind, searchable native IDs, Task snapshot, direct Message, a2a.StreamResponse-wrapped Event, and PrevVersion. Store its JSON as a string in CustomEntry.Extensions[a2a:record]. Derive recordId as SHA-256 over owner, kind, PrevVersion, canonical Task JSON, and canonical Event JSON so retries reproduce the same key.

Validate versions and native Task/Event identity before creating an entry.

**Step 5: Verify GREEN and commit**

Run:

    gofmt -w pkg/tape/a2a/record.go pkg/tape/a2a/record_test.go
    go test ./pkg/tape/a2a -run TestRecord -v
    go test ./...
    git add go.mod go.sum pkg/tape/a2a/record.go pkg/tape/a2a/record_test.go
    git commit -m "feat: add A2A tape record codec"

### Task 2: Create, Get, and recover authenticated Tasks

**Files:**

- Create: pkg/tape/a2a/store.go
- Create: pkg/tape/a2a/store_test.go

**Step 1: Write failing storage contract tests**

Run the same tests against JSONL and bbolt:

- Create returns a non-zero TaskVersion.
- Get returns a deep-equal Task and the same version.
- Closing/reopening the backend recovers the same StoredTask.
- duplicate Create returns taskstore.ErrTaskAlreadyExists.
- missing Get returns a2a.ErrTaskNotFound.
- owner B cannot Get owner A's Task, even with the same Task ID.

Use real storage implementations and authenticated contexts, not mocks.

**Step 2: Verify RED**

Run: go test ./pkg/tape/a2a -run 'TestStoreCreate|TestStoreRecovery|TestStoreOwner' -v

Expected: FAIL because Config, Store, and NewStore do not exist.

**Step 3: Implement the minimum Store**

Add:

    type Config struct {
        Storage storage.TapeStorage
        Authenticator taskstore.Authenticator
        TimeProvider func() time.Time
    }

    type Store struct {
        storage storage.TapeStorage
        authenticate taskstore.Authenticator
        now func() time.Time
        mu sync.Mutex
        initialized sync.Map
    }

NewStore rejects nil Storage or Authenticator. Each operation authenticates, rejects an empty principal with a2a.ErrUnauthenticated, derives owner.WithOwnerId(ctx, principal), and calls Storage.Init once per principal.

Create holds the mutex, replays that owner, rejects an existing Task ID, appends one a2a:task record, and returns the appended entry seq as TaskVersion. Get returns a JSON-deep-copy of the latest Task record.

**Step 4: Verify GREEN and commit**

Run:

    gofmt -w pkg/tape/a2a/store.go pkg/tape/a2a/store_test.go
    go test ./pkg/tape/a2a -run 'TestStoreCreate|TestStoreRecovery|TestStoreOwner' -v
    go test ./...
    git add pkg/tape/a2a/store.go pkg/tape/a2a/store_test.go
    git commit -m "feat: persist A2A tasks on Tape"

### Task 3: Add atomic Update, OCC, deduplication, and fail-closed replay

**Files:**

- Modify: pkg/tape/a2a/store.go
- Modify: pkg/tape/a2a/store_test.go

**Step 1: Write failing tests**

Add:

- TestStoreUpdateUsesOCC: stale PrevVersion returns taskstore.ErrConcurrentModification.
- TestStoreUpdateRetryHasNoSecondEffect: the same accepted request returns its prior version and does not append again.
- TestStoreUpdatePersistsTaskAndEventAtomically: reopening recovers the desired Task and its audit Event.
- TestStoreReplayFailsClosedOnCorruptRecord: malformed profile data returns an error containing seq and record identity.

**Step 2: Verify RED**

Run: go test ./pkg/tape/a2a -run 'TestStoreUpdate|TestStoreReplayFails' -v

Expected: FAIL because Update is missing or lacks the required behavior.

**Step 3: Implement Update**

Under Store.mu:

1. authenticate and replay the owner's records;
2. validate UpdateRequest, Task, and native Event;
3. derive deterministic recordId;
4. return the existing version if recordId already exists;
5. return a2a.ErrTaskNotFound when no Task exists;
6. compare non-zero PrevVersion and return ErrConcurrentModification on mismatch;
7. append one record containing the complete new Task and Event;
8. return the appended seq as TaskVersion.

Replay rejects unknown versions, malformed extension JSON, owner mismatches, and inconsistent native IDs. Format errors as a2a tape: replay seq N record ID: cause.

**Step 4: Verify GREEN, race safety, and commit**

Run:

    gofmt -w pkg/tape/a2a/store.go pkg/tape/a2a/store_test.go
    go test ./pkg/tape/a2a -run 'TestStoreUpdate|TestStoreReplayFails' -v
    go test -race ./pkg/tape/a2a
    go test ./...
    git add pkg/tape/a2a/store.go pkg/tape/a2a/store_test.go
    git commit -m "feat: add OCC and recovery to A2A tape store"

### Task 4: Implement owner-scoped List and pagination

**Files:**

- Create: pkg/tape/a2a/list.go
- Modify: pkg/tape/a2a/store.go
- Modify: pkg/tape/a2a/store_test.go

**Step 1: Write failing List tests**

Cover authenticated-owner isolation; ContextID, Status, and StatusTimestampAfter filters; default PageSize 50; invalid sizes outside 1..100; stable pagination by record timestamp then Task ID; HistoryLength truncation; and IncludeArtifacts behavior.

**Step 2: Verify RED**

Run: go test ./pkg/tape/a2a -run TestStoreList -v

Expected: FAIL because List is unimplemented.

**Step 3: Implement List**

Project only each Task's latest record for the authenticated owner. Sort newest record timestamp first, then Task ID descending. Encode timestamp and Task ID in an opaque base64 JSON page token. Deep-copy Tasks before response-only history/artifact trimming.

**Step 4: Verify GREEN and commit**

Run:

    gofmt -w pkg/tape/a2a/list.go pkg/tape/a2a/store.go pkg/tape/a2a/store_test.go
    go test ./pkg/tape/a2a -run TestStoreList -v
    go test ./...
    git add pkg/tape/a2a/list.go pkg/tape/a2a/store.go pkg/tape/a2a/store_test.go
    git commit -m "feat: list owner-scoped A2A tasks"

### Task 5: Persist direct taskless Messages

**Files:**

- Create: pkg/tape/a2a/interceptor.go
- Create: pkg/tape/a2a/interceptor_test.go

**Step 1: Write failing interceptor tests**

Test an a2asrv.CallInterceptor After call with a successful taskless Message response. Verify one a2a:message record is appended. Repeating the same messageId must not append again. Task responses, errored responses, and Messages with TaskID must not create direct-message records.

**Step 2: Verify RED**

Run: go test ./pkg/tape/a2a -run TestPersistenceInterceptor -v

Expected: FAIL because NewPersistenceInterceptor is undefined.

**Step 3: Implement the interceptor**

Embed a2asrv.PassthroughCallInterceptor. After returns early for response errors and non-Message payloads. For a taskless Message, authenticate, derive a deterministic recordId from owner and messageId, replay for deduplication, and append one a2a:message record.

**Step 4: Verify GREEN and commit**

Run:

    gofmt -w pkg/tape/a2a/interceptor.go pkg/tape/a2a/interceptor_test.go
    go test ./pkg/tape/a2a -run TestPersistenceInterceptor -v
    go test ./...
    git add pkg/tape/a2a/interceptor.go pkg/tape/a2a/interceptor_test.go
    git commit -m "feat: persist direct A2A messages"

### Task 6: Add the four Godog living-spec scenarios

**Files:**

- Create: pkg/tape/a2a/features/tape_store.feature
- Create: pkg/tape/a2a/bdd_test.go

**Step 1: Add the approved feature without step definitions**

Copy the four scenarios from docs/plans/2026-08-03-a2a-tape-storage-profile-design.md exactly: restart, concurrent/repeated update, tenant isolation, and REST/JSON-RPC two-agent delegation.

**Step 2: Verify RED**

Run: go test ./pkg/tape/a2a -run TestFeatures -v

Expected: FAIL with undefined Godog steps.

**Step 3: Implement storage scenario steps**

Use per-scenario featureState. Register steps with godog.ScenarioContext. Use real JSONL/bbolt backends, real close/reopen, concurrent Update calls, and two authenticated principals.

Run the feature test and confirm the first three scenarios pass while transport steps still fail.

**Step 4: Implement the two-agent transport steps**

Create a deterministic test executor implementing a2asrv.AgentExecutor that emits a submitted Task, Artifact, and completed status. Start httptest.Server with a2asrv.NewHandler and child TapeTaskStore, wrapped in NewJSONRPCHandler. Use the official a2aclient to SendMessage, reopen child storage, GetTask, and assert the Artifact is consumable.

Owner scope adjustment (2026-08-04): REST implementation and verification are deferred. The first transport proof is JSON-RPC only.

**Step 5: Verify GREEN and commit**

Run:

    gofmt -w pkg/tape/a2a/bdd_test.go
    go test ./pkg/tape/a2a -run TestFeatures -v
    go test ./...
    git add pkg/tape/a2a/features/tape_store.feature pkg/tape/a2a/bdd_test.go
    git commit -m "test: specify A2A tape behavior with Godog"

### Task 7: Documentation, full verification, and review

**Files:**

- Create: pkg/tape/a2a/doc.go
- Modify: docs/plans/2026-08-03-a2a-tape-storage-profile-design.md

**Step 1: Add package and implementation notes**

Document authenticated-principal requirements, a2asrv.WithTaskStore installation, the direct-message interceptor, private Tape seq semantics, and pinned dependency versions. Mark the design as implemented on this feature branch.

**Step 2: Run fresh full verification**

Run:

    gofmt -w pkg/tape/a2a
    go vet ./...
    go test ./...
    go test -race ./pkg/tape/a2a
    git diff --check
    git status --short

Expected: vet and tests pass without warnings, race reports no races, diff check is empty, and status contains only intended documentation changes.

**Step 3: Commit documentation**

Run:

    git add docs/plans/2026-08-03-a2a-tape-storage-profile-design.md pkg/tape/a2a/doc.go
    git commit -m "docs: document A2A tape store integration"

**Step 4: Request review**

Use @requesting-code-review on the full branch diff from 12c9283. Address every finding with a new failing test before any production fix.
