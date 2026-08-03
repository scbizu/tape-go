# A2A 1.0 Tape Storage Profile

Status: accepted design

Date: 2026-08-03

## Purpose

This document defines how a Tape-backed agent persists A2A 1.0 work. It is a
storage profile, not a new agent protocol. A2A remains the source of truth for
the `Task`, `Message`, `Artifact`, task lifecycle, and server operations. Tape
provides an append-only, owner-isolated record from which an A2A task store can
be rebuilt after a restart.

The first implementation must prove that two agents with independent Tape
implementations can communicate through standard A2A while each agent retains
its own storage and identity boundary.

## Design principles

- Preserve native A2A 1.0 objects. Do not create Tape-specific Task or status
  models.
- Persist before publishing. An update is visible to a response, stream, or
  push subscriber only after its Tape append succeeds.
- Treat authentication as authoritative. `contextId`, `taskId`, and metadata
  supplied by a peer never determine the local Tape owner.
- Keep local sequence numbers private. A Tape seq may back an internal task
  version or cursor but is not an interoperable identifier.
- Make recovery deterministic. Replaying the same accepted records produces the
  same current Task, history, artifacts, and version.
- Keep the profile thin. Transport, execution, discovery, and orchestration stay
  in A2A and its SDK.

## Native A2A model

The profile stores these A2A objects without renaming or reshaping their fields:

- `Message`: a communication unit with a creator-generated `messageId` and
  optional `contextId` and `taskId`.
- `Task`: the server-generated stateful unit of work, containing current status,
  artifacts, and optional message history.
- `TaskStatusUpdateEvent`: a task status transition.
- `TaskArtifactUpdateEvent`: an artifact update or chunk.

A simple A2A request may return a direct `Message` without creating a `Task`.
The storage profile must preserve that distinction and must not synthesize a
Task for message-only interactions.

## Record format

Each accepted mutation is stored as one Tape custom entry. The storage envelope
contains only profile metadata and native A2A payloads:

```text
TapeA2ARecord
  profile_version   storage profile version, initially "1"
  a2a_version       negotiated A2A major/minor version, initially "1.0"
  record_id         adapter-generated idempotency key, stable across retries
  kind              a2a:task | a2a:message | a2a:status_update |
                    a2a:artifact_update
  context_id        optional searchable copy of native contextId
  task_id           optional searchable copy of native taskId
  message_id        optional searchable copy of native messageId
  artifact_id       optional searchable copy of native artifactId
  task_snapshot     native A2A Task after the mutation, when a Task exists
  direct_message    native A2A Message for a message-only response
  event             native A2A Event that caused a Task update
```

Native objects are encoded with A2A ProtoJSON rules. The searchable ID copies
must match the native payload; a mismatch is rejected before append. The owner
or authenticated principal is not accepted from this envelope. It is derived
from the server call context and enforced through Tape's owner isolation.

`recordId` exists because A2A status and artifact update events do not have an
independent event ID. The adapter creates it on first ingestion and reuses it
for retries. A repeated `recordId` has no second effect. A repeated `messageId`
must return the original processing result rather than execute the request
again.

## Atomic persistence and replay

Task creation appends an `a2a:task` record containing the initial native Task.
Every later task mutation appends exactly one record containing both:

1. the complete Task snapshot after the mutation; and
2. the native A2A Event that triggered it.

Keeping snapshot and event in one entry avoids a partial commit between audit
history and recoverable state. The snapshot is authoritative for recovery; the
event preserves provenance and supports validation. A direct message-only
response appends an `a2a:message` record with `directMessage` and no Task.

Replay proceeds in Tape order. The latest valid snapshot for each Task becomes
the current task state and version. The accompanying events are checked for
consistent task/context/artifact identities. An implementation may build an
index or projection cache, but it must be rebuildable entirely from Tape.

Artifact chunks are not dropped. The stored post-update Task snapshot must
reflect the SDK's application of `append` and `lastChunk`. Recovery therefore
does not depend on a live stream or event queue.

## Go integration

The implementation should provide `TapeTaskStore`, implementing the official
Go SDK interface:

```go
type Store interface {
	Create(context.Context, *a2a.Task) (taskstore.TaskVersion, error)
	Update(context.Context, *taskstore.UpdateRequest) (taskstore.TaskVersion, error)
	Get(context.Context, a2a.TaskID) (*taskstore.StoredTask, error)
	List(context.Context, *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error)
}
```

It is installed with `a2asrv.WithTaskStore`. The A2A server stack continues to
own request handling, event application, streaming, push delivery, cancellation,
and task lifecycle validation.

`TapeTaskStore` has three internal responsibilities:

- Codec: validate profile version, A2A version, native object consistency, and
  ProtoJSON encoding.
- Appender: resolve the authenticated owner/tenant and atomically append a
  record to that owner's Tape.
- Projector: rebuild `StoredTask{Task, Version}` and implement Get/List filtering.

The first implementation may scan and replay records. Secondary indexes are a
performance optimization and must not become another source of truth.

## Concurrency and failure semantics

The store preserves the SDK's optimistic concurrency control. `Update` compares
`PrevVersion` with the current stored version. A mismatch returns
`taskstore.ErrConcurrentModification`; the official server stack decides whether
to retry or cancel the execution. A committed Tape seq may serve as the internal
`TaskVersion` if comparison remains monotonic for that Task.

The following rules are mandatory:

- Unknown profile or A2A versions, malformed native payloads, ID mismatches, and
  ACL failures are rejected before append.
- A failed append fails the request and prevents response, stream, or push
  publication of that mutation.
- A response, stream, or push delivery failure does not roll back a committed
  record. The client recovers through GetTask or SubscribeToTask.
- Replay encountering a corrupt or inconsistent record fails closed and reports
  its Tape seq and `recordId`. It must not silently skip the record and return a
  fabricated current state.
- Terminal-state and cancellation behavior remains A2A SDK behavior; the
  profile does not define a second state machine.

## BDD acceptance specification

Use `github.com/cucumber/godog`, pinned in `go.mod`, so the Gherkin feature is
both acceptance test and living protocol documentation. Keep the suite to four
behaviors:

```gherkin
Feature: persist A2A tasks on Tape

  Scenario Outline: task survives restart
    Given a <storage> TapeTaskStore
    When an A2A task advances to <state> and the store restarts
    Then GetTask returns the same task, history, artifacts and version

    Examples:
      | storage | state          |
      | jsonl   | working        |
      | jsonl   | input_required |
      | jsonl   | completed      |
      | bbolt   | working        |
      | bbolt   | input_required |
      | bbolt   | completed      |

  Scenario: concurrent and repeated updates are safe
    Given a stored task version
    When two updates use that version and one accepted record is retried
    Then exactly one competing update succeeds and the retry has no second effect

  Scenario: tenant isolation is preserved
    Given two authenticated principals use the same task id
    Then neither principal can Get or List the other principal's task

  Scenario Outline: standard A2A clients see the same result
    Given parent and child agents use independent Tapes
    When the parent delegates through <transport>
    Then the child task remains queryable after restart
    And the parent can consume the child artifact

    Examples:
      | transport |
      | REST      |
      | JSON-RPC  |
```

ProtoJSON round trips, mismatched IDs, corrupt-record handling, and artifact
chunk folding remain focused table-driven Go unit tests. They are implementation
rules, not additional BDD scenarios.

## Non-goals for the first implementation

- Replicating Tape entries or sequence numbers between agents.
- A shared Tape or shared task store across independent agents.
- Cluster mode, distributed work queues, or persistent push configuration.
- A TapeCapsule migration format.
- Derived-agent metrics or automatic sub-agent orchestration.
- A custom transport, Task type, state machine, or A2A extension.

The two-agent acceptance scenario proves standard delegation across independent
Tapes. Lineage, orchestration policy, and metrics are separate follow-up designs.

## Completion criteria

The future implementation is ready for review when:

1. `TapeTaskStore` satisfies the official `taskstore.Store` interface.
2. The four Godog scenarios pass against the declared storage/transport examples.
3. Focused unit tests cover codec validation, corruption, and artifact folding.
4. The server exposes standard A2A 1.0 behavior without a Tape-specific wire
   protocol or public local sequence IDs.

## References

- A2A specification: https://github.com/a2aproject/A2A/blob/main/docs/specification.md
- A2A protocol schema: https://github.com/a2aproject/A2A/blob/main/specification/a2a.proto
- Official Go SDK server: https://pkg.go.dev/github.com/a2aproject/a2a-go/v2/a2asrv
- Official Go SDK task store: https://pkg.go.dev/github.com/a2aproject/a2a-go/v2/a2asrv/taskstore
- Godog: https://pkg.go.dev/github.com/cucumber/godog
- A2A testing projects: https://github.com/a2aproject/a2a-samples
