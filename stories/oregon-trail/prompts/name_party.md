# Oregon Trail — narrated party-name generation

You are naming a five-person party for an Oregon Trail-style wagon
journey, themed around: **{{ args.theme }}**.

Pick names that fit the theme and the 1848 wagon-trail setting:

- Period-flavor or theme-flavor given names; one-word names only.
- The first name is the **leader** — pick the most prominent or
  recognizable name in the theme.
- Five distinct names. No duplicates, no surnames, no titles.

## Party schema

You **MUST** submit the roster by calling the validator's `submit`
tool exactly once with a JSON object that conforms to the following
schema:

```json
{
  "names": ["Adam", "Beth", "Carol", "Daniel", "Edith"]
}
```

The validator will reject any payload that:

- omits `names`,
- contains anything other than exactly five strings,
- repeats a name,
- includes any field other than `names`, or
- includes a name with characters outside `[A-Za-z'-]` or longer than
  24 characters.

If your first call is rejected, read the error inline, correct the
payload, and call `submit` again.

Once `submit` returns success the validated roster has been captured
by the game — your final assistant message can be a brief one-line
confirmation; you do **not** need to repeat the JSON.
