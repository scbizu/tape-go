# Project documentation

- Store architecture and technical design documents in `docs/designs/`.
- Each design must contain its written specification and a Mermaid design
  diagram together in one Markdown file.
- Use descriptive filenames without a leading date (for example,
  `a2a-tape-storage-profile-design.md`, not `2026-08-03-a2a-tape-storage-profile-design.md`).
- Store project plans in Obsidian under `anra-studio/tape-go/`, using the Obsidian
  MCP tools. Do not add implementation plans or task checklists to Git.
- Before removing a repository plan, migrate its full content through Obsidian
  MCP and read it back to verify the migration. Preserve architecture designs
  in `docs/designs/` with their Mermaid diagrams.

## Document ownership

Git holds architecture and technical design specifications: the problem,
constraints, decisions, interfaces, invariants, failure behavior, and
compatibility boundaries needed to understand and maintain the implementation.
Keep each specification and its Mermaid design diagram in one Markdown file
under the design directory. Update both when the design changes.

Obsidian holds project plans: proposed work, implementation steps, task
checklists, progress, and execution notes. Read and maintain these through the
Obsidian MCP tools under the configured project path. A document containing
both a design and a plan should have its durable design extracted into Git
while its planning content stays in Obsidian.

Use descriptive filenames without leading date prefixes in both locations.
Dates may remain in document metadata or text when they provide useful context.
Update references when a document moves or is renamed.

## Creating and updating documents

Classify the content by its purpose before choosing its location. For a design,
write the specification and an accompanying Mermaid diagram that explains its
components, interactions, or state transitions. For a plan, inspect the
Obsidian destination before writing; preserve existing notes and use guarded
updates rather than overwriting unseen content.

Maintain links between a plan and the relevant repository design when useful.
Keep implementation task lists in Obsidian even when they refer to a design
stored in Git. Exclude repository planning directories with `.gitignore`;
ignore rules alone do not remove files already tracked by Git.

## Migrating existing documents

Before removing a repository plan, inspect its full content and check the
destination for naming collisions. Create or safely update the Obsidian note
through MCP, then read it back and verify that the full content was preserved.
Account separately for any intentional reference changes.

For mixed documents, retain the architecture specification in the design
directory and add its Mermaid diagram. Preserve the original document in
Obsidian before removing the repository copy. Remove the repository plan only
after successful read-back verification, then update references and ignore
rules. If MCP is unavailable or verification fails, retain the source and
report the incomplete migration.

## Verification

Check that every design includes its written specification and Mermaid diagram,
filenames have no leading date, and document links resolve to the intended
destinations. Confirm that migrated notes can be read through Obsidian MCP,
repository plans are removed from the pending Git changes, and planning
directories are ignored. Review the diff and run `git diff --check` before
reporting completion.
