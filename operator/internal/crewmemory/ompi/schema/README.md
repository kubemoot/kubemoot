# Schemas for the crew memory export

These JSON Schemas are Kubemoot's own. They are derived from the field table
(section 3.2), provenance (section 4), version (section 6.1) and manifest
(section 8.1) of the Open Memory Protocol Initiative (OMPI) exchange-and-runtime
working spec v0.2, the editors' draft of 2026-10-14 (OMPI pull request #13, commit
`20235eaa9f3cb27cdce9e86667aa56ac963abeae`, file
`working-group/exchange-and-runtime/2026-10-14-working-spec-v0.2.md`).

The spec is a draft and has no official validator, so this is not an OMPI
artifact and does not certify conformance. It checks the minimal profile
(section 10.1: `identifier`, `media_type`, `created_at`, a body) and the types of
every optional field. Additional properties are allowed because the spec lets an
implementation add fields. When the spec changes, these files and the pinned
reference in `types.go` change with it.
