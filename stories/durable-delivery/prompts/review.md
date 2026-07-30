Independently review the repair described by `{{ payload }}`.

Pass only if the fix preserves behavior and no test, policy, gate, assertion,
timeout, or coverage threshold was weakened. This is read-only review: do not
edit files. On pass, echo the exact reviewed `bundle_ref`, `bundle_digest`, and
`bundle_kind` from the payload together with a concise reason and trace
reference; never substitute a different artifact.
