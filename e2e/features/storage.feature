Feature: Tape history survives a restart
  A conversation must retain its source entries and their identity,
  regardless of which persistent storage backend is used.

  Scenario Outline: Persist the Lantern Road conversation
    Given a "<backend>" tape
    And the Lantern Road fixture with 100 source entries in 20 scenes
    When the conversation is recorded scene by scene
    Then all source entries retain their IDs, kinds, content and order
    When the tape is closed and reopened
    Then all source entries retain their IDs, kinds, content and order
    And all recorded anchors retain their IDs, kinds and content

    Examples:
      | backend |
      | jsonl   |
      | bbolt   |
