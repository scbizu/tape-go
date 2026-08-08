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

  Scenario: standard A2A client sees the same result over JSON-RPC
    Given parent and child agents use independent Tapes
    When the parent delegates through JSON-RPC
    Then the child task remains queryable after restart
    And the parent can consume the child artifact
