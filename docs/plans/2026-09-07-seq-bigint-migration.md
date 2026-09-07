# Arbitrary-Precision Seq Migration

## Invariants

- `entry.Seq` is immutable and value-comparable.
- Zero is the only unassigned sentinel; stored entries use positive sequences.
- Positive sequences are canonical unsigned decimal integers with no fixed-width limit.
- Ordering is numeric, and successor calculation never wraps.
- JSON writes and reads sequences only as canonical decimal strings.
- Half-open ranges remain `[SeqS, SeqE)` and use `Seq.Next()` for exclusive ends.
- Public command argument structs use `entry.Seq` directly. Sequence parsing and canonical-value validation belong to the Seq codec; the ADK adapter only overrides schema inference to describe the decimal-string wire shape because the value's representation is intentionally private.

## Storage migration

JSONL files use decimal-string sequences only. Files containing the former numeric representation are unsupported.

bbolt entry and anchor keys use a versioned, numerically ordered encoding composed of a fixed marker, a big-endian magnitude length, and the big-endian magnitude. There is no legacy-key migration path; existing stores with eight-byte `uint64` keys are unsupported and callers must start with a new store.

This is a clean format break. Older binaries cannot read the new JSONL or bbolt representation, and the new implementation does not read or rewrite old sequence data.

## A2A versions

`taskstore.TaskVersion` is a separate projected domain. A2A profile v1 keeps `PrevVersion` for OCC, projects Create as version 1, and increments once per later record for the same Task. Versions never derive from Tape Seq and need no additional persisted `Version` field. There is no profile v2.

## Compatibility boundary

Source compatibility is intentionally broken wherever callers used raw integer sequence literals. Persisted data using the former numeric JSON or eight-byte bbolt key representation is intentionally unsupported; callers must start with new stores.

Known downstream consumers must migrate before updating their tape-go dependency. In particular, Anra at `39faef1` still stores projected sequences as `uint64`, compares them with integer operators, constructs ranges from integer literals, and exposes `SubmitResult.Seq` as `uint64`. Its matching migration should use `entry.Seq`, `Cmp`, `Next`, and decimal strings at external JSON boundaries.
