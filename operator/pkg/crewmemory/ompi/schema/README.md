# Schemas for the crew memory export

These JSON Schemas are Kubemoot's own. They are derived from the field table
(section 1.3), provenance and evidence (section 3.1), version (section 3.3.2) and
the manifest working text (Notes D.1) of the Open Memory Protocol Initiative (OMPI)
exchange-and-runtime working spec v0.6, the editors' draft of 2026-11-09 (commit
`5fd045c5a492fd51aca4a65cdf817d5a9f37c8a1`, file
`working-group/exchange-and-runtime/2026-11-09-working-spec-v0.6.md`).

The spec is a draft and has no official validator, so this is not an OMPI
artifact and does not certify conformance. It checks the minimal profile
(section 3.7: `identifier`, `media_type`, `created_at`, a body) and the types of
every optional field. Additional properties are allowed because the spec lets an
implementation add fields.

It follows the spec's unmarked and agreed text. Text marked "Proposed" (for
example the scalar `version`, `body_encoding`, tombstones) is not adopted and the
schema does not require it; a `version` is accepted but not required, as a scalar or as the
`{value, comparator}` object the spec's example uses. The spec says a validator
must not infer a requirement its field table does not state, so no field is
required beyond the table. When the spec changes, these files and the pinned
reference in `types.go` change with it.
