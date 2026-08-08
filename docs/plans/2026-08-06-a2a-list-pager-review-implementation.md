# A2A List Pager Review Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Address both unresolved PR #2 review threads by restoring direct List page-size validation and extracting cursor pagination into a private pager.

**Architecture:** A package-private `taskPager` owns page-size normalization, token decoding, page boundary selection, and next-token encoding. `Store.List` continues to filter and sort owner-scoped tasks, then delegates pagination without changing its public behavior.

**Tech Stack:** Go 1.24, A2A Go SDK v2.3.1, standard library testing, JSONL and bbolt Tape backends.

---

### Task 1: Specify native pager validation

**Files:**
- Modify: `pkg/tape/a2a/store_test.go`
- Modify: `pkg/tape/a2a/list.go`

**Step 1: Write the failing test**

Add `TestNewTaskPagerNormalizesAndValidatesPageSize`. Assert that size `0` becomes `defaultPageSize`, size `100` is accepted unchanged, and `-1`/`101` return errors wrapping `a2a.ErrInvalidRequest`.

**Step 2: Run the focused test and verify RED**

Run: `go test ./pkg/tape/a2a -run '^TestNewTaskPagerNormalizesAndValidatesPageSize$'`

Expected: FAIL because `newTaskPager` does not exist.

**Step 3: Implement the minimum constructor**

Add a private `taskPager` and `newTaskPager(pageSize int, token string)`. Use explicit bounds checks and default normalization. Decode a non-empty token with `decodePageCursor`.

**Step 4: Run the focused test and verify GREEN**

Run: `go test ./pkg/tape/a2a -run '^TestNewTaskPagerNormalizesAndValidatesPageSize$'`

Expected: PASS.

### Task 2: Specify pager slicing and cursor behavior

**Files:**
- Modify: `pkg/tape/a2a/store_test.go`
- Modify: `pkg/tape/a2a/list.go`

**Step 1: Write the failing test**

Add `TestTaskPagerPagesSortedItems`. Build three already-sorted `listItem` values, request page size two, assert the first page contains two items and a token, then construct the next pager from that token and assert the second page contains the remaining item with no token.

**Step 2: Run the focused test and verify RED**

Run: `go test ./pkg/tape/a2a -run '^TestTaskPagerPagesSortedItems$'`

Expected: FAIL because the pager has no page method.

**Step 3: Implement the minimum page method**

Move cursor-start selection, page truncation, and next-token encoding from `Store.List` into `taskPager.page(items []listItem)`.

**Step 4: Run both pager tests and verify GREEN**

Run: `go test ./pkg/tape/a2a -run 'Test(NewTaskPager|TaskPager)'`

Expected: PASS.

### Task 3: Refactor Store.List under characterization coverage

**Files:**
- Modify: `pkg/tape/a2a/list.go`
- Test: `pkg/tape/a2a/store_test.go`

**Step 1: Wire the pager**

Construct the pager before owner access, keep filtering and sorting unchanged, record `TotalSize` before paging, delegate to `pager.page`, and return `pager.pageSize` in the response.

**Step 2: Run focused List tests**

Run: `go test ./pkg/tape/a2a -run 'Test(StoreList|ListItem|TaskPager|NewTaskPager)'`

Expected: PASS.

**Step 3: Run package race coverage**

Run: `go test -race ./pkg/tape/a2a`

Expected: PASS.

### Task 4: Verify and publish

**Files:**
- Modify only if verification exposes a defect.

**Step 1: Run full verification**

Run: `go vet ./...`

Run: `go test ./...`

Run: `go mod verify`

Run: `git diff --check`

Expected: all commands exit successfully.

**Step 2: Review the diff**

Run: `git diff --stat`

Run: `git diff`

Confirm both review requests are addressed without unrelated changes.

**Step 3: Commit and push**

Commit the review changes with a focused message and push `feature/a2a-tape-store`.

**Step 4: Re-read GitHub threads**

Use the bundled thread-aware fetch script to confirm the two threads now point at superseded code or remain available for the owner to resolve. Do not reply or resolve them without explicit authorization.
