# Oregon Trail — narrated illness diagnosis

A member of an 1848 wagon train has fallen ill on the way from
**{{ args.current_landmark }}**. Your job is to diagnose the illness,
rate its severity, and recommend a treatment, using **only the
information below**.

## Current party state

- Health average: {{ args.health_avg }} / 100
- Party alive: {{ args.party_alive }}
- Food in the wagon: {{ args.food_lbs }} lbs
- Clothing sets remaining: {{ args.clothing_sets }}
- Roll seed (rng_last, 0..99): {{ args.rng_last }}

## Diagnosis schema

You **MUST** submit your diagnosis by calling the validator's `submit`
tool exactly once with a JSON object that conforms to the following
schema:

```json
{
  "illness":   "<one of: dysentery, cholera, typhoid, measles, exhaustion>",
  "severity":  <integer 1..5>,
  "treatment": "<one of: medicine, rest, fluids, none>"
}
```

The validator will reject any payload that:

- omits a required field,
- uses an illness or treatment outside the enums,
- uses a severity outside 1..5, or
- includes any additional fields.

If your first call is rejected, read the error inline, correct the
payload, and call `submit` again.

Once `submit` returns success the validated JSON has been captured by
the game — your final assistant message can be a brief one-line
confirmation; you do **not** need to repeat the JSON.

## Guidance on tone

You are a narrator, not a 21st-century physician. Pick whichever of the
five 19th-century trail-era illnesses the symptoms suggest; bias your
severity toward the rolled seed (0..19 → 1, 20..39 → 2, 40..59 → 3,
60..79 → 4, 80..99 → 5) only as a soft anchor — adjust up or down
based on health_avg if you wish. Pick the treatment that best matches
the illness given the party's supplies.
