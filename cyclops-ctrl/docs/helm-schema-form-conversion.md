# Helm values schema → form field conversion

This document covers the extension of the Helm `values.schema.json` → `models.Field`
conversion in `internal/mapper` and `internal/models/helm`. It records the
compatibility decisions, the supported/ignored keyword set, and the call sites
that moved to the diagnostics-returning API.

## Entry points

| Function | Purpose |
| --- | --- |
| `mapper.HelmSchemaToFields(name, schema, defs, dependencies) models.Field` | Unchanged signature. Returns only the field tree. Kept for existing callers. |
| `mapper.HelmSchemaToFieldsWithDiagnostics(name, schema, defs, dependencies) (models.Field, []mapper.Diagnostic)` | New. Returns the field tree plus conversion diagnostics. |

`Diagnostic` has `Level` (`warning`/`error`), `Path` (dotted schema path, e.g.
`root.storage.items`), `Keyword`, and `Message`. The returned slice is sorted
deterministically (path, keyword, message, level) so the same schema always
produces byte-identical output and diagnostics.

### Call sites migrated to the diagnostics-returning version

- `pkg/template/helm.go` — `Repo.mapHelmChart` (Helm OCI registry charts)
- `pkg/template/git.go` — `Repo.mapHelmChart` (Git-sourced charts)
- `internal/controller/helm.go` — `Helm.GetReleaseSchema` (existing releases)

All three log the diagnostics (`fmt.Printf`/`fmt.Println`) and keep serving the
field tree. The old `HelmSchemaToFields` stays available; tests and any other
caller continue to compile unchanged.

## Compatibility

### `exclusiveMinimum` / `exclusiveMaximum` (draft 4 vs draft 6+)

`helm.Property` previously declared these as `*bool` (the draft-4 form). Draft 6
and later encode them as numbers, so a schema using the numeric form failed to
deserialise and the whole form was lost. Both encodings are now accepted:

- `"exclusiveMinimum": true` (draft 4) keeps working, and is still emitted to the
  frontend as a boolean alongside `minimum`, exactly as before.
- `"exclusiveMinimum": 5` (draft 6+) sets `minimum` to the effective lower bound
  and marks it exclusive. Combined with an inclusive `minimum`, the tighter bound
  wins; `x > 5` with `minimum: 10` yields the inclusive `10`.
- Any other value (e.g. a string) is rejected during deserialisation.

`models.Field` keeps its `*bool` `ExclusiveMinimum`/`ExclusiveMaximum` contract so
the frontend validators are untouched.

## Composition

### `oneOf` / `anyOf`

Each composition becomes a field with `variants` — each variant has a `title` and
its own field subtree — so the frontend can render a selector. `oneOf` and
`anyOf` keep their own candidate sets; they are never merged together. If a
schema declares both, `oneOf` is rendered and `anyOf` is reported as ignored.

- **Discriminator**: the converter looks for a property that every non-null
  branch pins to a constant (`const` or a single-valued `enum`) with distinct
  values. When found, `discriminator` names that property and variant titles are
  the constant values (e.g. `type: {const: "s3"}` → title `s3`). Candidate
  property names are tried in sorted order for determinism.
- **No discriminator**: variant titles are `Variant 1`, `Variant 2`, …, the
  field carries `variant_note: "no discriminator"`, and `has_discriminator` is
  false.
- **Null branches**: a branch whose type is only `null` is dropped (it is the
  "optional" marker), preventing the "first branch" rule from producing a
  `null`-typed field. The drop is reported.
- **Boolean preference**: the legacy "if a branch is boolean, use boolean" rule is
  kept **only** for the boolean+object combination it was designed for. In any
  other mix (e.g. boolean+string) the field becomes variants, so no object or
  other branch is lost.
- **Sibling properties**: `properties`/`required` declared next to `oneOf`/`anyOf`
  apply to every branch and are folded into each variant.

**Boundary — candidate selection, not validation:** the form only offers the
candidates. `oneOf` (exactly one branch valid) vs `anyOf` (one or more valid) is
*not* enforced here; that is a validation concern. The field tree carries both as
variants and leaves the distinction to the consumer.

### `allOf` (merge)

`allOf` is merged: each branch's `properties` and `required` are unioned. When the
same property name is declared with conflicting types, a diagnostic error naming
the path (e.g. `root.resource.replicas`) is produced — the latter branch does not
silently overwrite the former.

## Conditions

`if`/`then`/`else` and `dependentRequired`/`dependencies` express "a field
appears or becomes required only when another field has a certain value". These
are represented by an optional `FieldCondition` on a field, supporting only the
"equals a constant" / "is present" forms:

- `if: {properties: {x: {const: v}}}` + `then` → `condition: {path: x, value: v}`
  (also accepts a single-valued `enum` on `x`).
- `if: {required: [x]}` + `then` → `condition: {path: x, present: true}`.
- `dependentRequired: {x: [y]}` → field `y` gets `condition: {path: x,
  present: true, required: true}`.
- The draft-7 `dependencies` array form is treated like `dependentRequired`.

A `condition` with `required: true` means "visible, mandatory only while the
condition holds"; without it, "visible only while the condition holds".

**Not converted, reported instead:** richer `if` expressions (multiple
properties, `not`, comparisons), `then`/`else` that constrain already-declared
properties, and negated `else`. The affected fields are **kept visible** and a
warning is emitted, so a user is never misled into thinking a conditional field
is always shown.

## Arrays, enums, `const`

- Array items are converted with the same `$ref`/composition rules as any other
  schema node.
- An object item's `required` is carried on the item field, as before.
- An item with no `type` and no `$ref` still degrades to `string` (unchanged).
- Mixed string/number `enum` values are preserved verbatim; the field type comes
  from `type`.
- A single-valued `enum` or a `const` is kept **visible but read-only**
  (`read_only: true`). This was the recommended option and is the implemented
  behaviour; revisit if the product wants it hidden.

## `$ref`

Both `#/$defs/...` (draft 2020-12) and `#/definitions/...` (draft 7) are resolved,
including nested paths such as `#/$defs/a/b`. After the registry prefix the path is
walked level by level, preferring a node's `$defs` (then `definitions`) over its
`properties`, instead of switching to `properties` after the second segment. An
unresolvable reference produces an `error` diagnostic with the schema path and
still yields a field (typed `object`) rather than an empty field.

## Recursion limit

Conversion depth is capped at `maxConversionDepth = 16` across nested properties,
array items, composition branches and `$ref`/`allOf` chains. Beyond it the subtree
is truncated and a warning is emitted. Without the cap, a tree schema that
references itself would recurse until the process overflows its stack.

## Ignored keywords (reported, never silently dropped)

`not`, `patternProperties`, `prefixItems`, `contains`, `propertyNames`,
schema-valued `additionalProperties`, schema-valued `unevaluatedProperties`,
schema-valued `unevaluatedItems`, and schema-form `dependencies` are recognised
and reported as `warning`s. Boolean `additionalProperties`/`unevaluated*` are not
reported (they carry no form semantics).

## Ordering and determinism

`order` still lists fields first (in array order); unlisted fields follow in name
order. Properties are iterated in sorted order and diagnostics are sorted, so the
same schema converts to byte-identical output and diagnostics on every run
(covered by a test).
