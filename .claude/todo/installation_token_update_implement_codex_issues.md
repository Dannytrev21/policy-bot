# Integration Test Issues (go test ./...)

## Failing tests
- `policy.TestConfigMarshalYaml` cases (`withChangedFiles`, `author`, `modifiedLines`, etc.) failed because expected YAML fixtures do not include null fields, while actual output serializes many `null` entries (e.g., `only_changed_files`, `has_contributor_in`, `targets_branch`, etc.).

## Details
- Command: `go test ./...` (120s timeout hit once; rerun shows failures in policy package)
- Sample diff (from `withChangedFiles`): expected only `changed_files.paths`, actual includes many `...: null` fields under `if`.
- Similar diffs for `author`, `modifiedLines`, `author_is_only_contributor` variants; indicates marshal logic now emits optional fields as null instead of omitting them.

## Impact
- Prevents full test suite from passing; handler-specific suite remains green.

## Suggested next steps
1) Update YAML marshal options or omit empty fields to avoid writing null for unset predicates; or
2) Adjust test expectations if emitting null is intentional.

No handler/auth-refresh changes appear implicated directly; issue sits in `policy` package marshaling.
