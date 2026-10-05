@live
Feature: Retrieve earlier conversation context with JEV
  Scene checkpoints describe archived context. A query must recover its
  expected source entry without mixing in unrelated scenes.
  These scenarios call the real JEV service.

  Scenario Outline: Retrieve the Lantern Road before and after restart
    Given a "<backend>" tape
    And the Lantern Road fixture with 100 source entries in 20 scenes
    And JEV checkpointing and retrieval are enabled
    When the conversation is recorded scene by scene
    Then all source entries retain their IDs, kinds, content and order
    And at least 20 anchors each describe a distinct complete scene
    And all 20 golden queries return their expected source without unrelated scenes
    When the tape is closed and reopened
    Then all source entries retain their IDs, kinds, content and order
    And all recorded anchors retain their IDs, kinds and content
    And all 20 golden queries return their expected source without unrelated scenes

    Examples:
      | backend |
      | jsonl   |
      | bbolt   |
