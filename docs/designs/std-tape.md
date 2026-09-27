Stand Tape(Go) Structure
---

## Basics

- A Stand Tape includes the basic [tape](https://tape.systems) (pkg/tape) implementation .
- A Stand Tape designed for multiple-tenant (pkg/owner) by default.
- A Stand Tape depends adk-go (pkg/agent) for general agent runtime(built-in) , but other agent runtime framework implement runner.Runner should be valid.
- A Stand Tape has a finder (pkg/tape/finder) to integrate with different search algorithms for different scenarios.
