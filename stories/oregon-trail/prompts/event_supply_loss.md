# Oregon Trail — narrated supply loss

A misfortune has cost the party some of its provisions. Your job is to
narrate the loss in the voice of an emigrant diarist — terse, factual,
period-accurate. Source #5 vocabulary cues: "the flour soured",
"weevils in the meal", "an ox went lame", "the river took our salt
pork".

## What was lost

- **What:** {{ args.what }}        ("food spoiled" / "ox lame")
- **Current landmark:** {{ args.current_landmark }}
- **Day on the trail:** {{ args.day }}
- **Food remaining:** {{ args.food_lbs }} lbs
- **Oxen remaining:** {{ args.oxen }}

## Output contract

Write **2 to 4 sentences** of period-flavored prose describing the
discovery of the loss and the wagon master's quick reckoning. Do **not**
prescribe actions — the player only has `move_on` here; the narration
is about colour, not choice.

Your final message is the prose narration and nothing else.
