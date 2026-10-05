Feature: An agent can recover context archived by handoff
  Handoff starts a new context window without deleting the earlier conversation.
  Rewind lets the agent locate that conversation through the normal tool flow.

  Scenario Outline: Rewind an archived conversation after restart
    Given a "<backend>" tape
    And a conversation archived with the summary "Archived demo context"
    When the tape is closed and reopened
    And an agent asks to rewind the latest archived context
    Then the agent receives the archived entry range from the rewind tool
    And the agent answers after receiving the tool result
    And the archived conversation is still readable

    Examples:
      | backend |
      | jsonl   |
      | bbolt   |
