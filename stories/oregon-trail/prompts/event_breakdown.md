# Oregon Trail — narrated wagon breakdown

A wagon part has failed on the trail in 1848. Your job is to narrate the
breakdown in the voice of an emigrant diarist (Source #5 vocabulary —
"axle tree broken", "hub split", "tongue rived"). Keep it grounded in
the supplies on hand.

## What broke

- **Part:** {{ args.part }}        (wheel / axle / tongue)
- **Spares remaining of that part:** {{ args.spares_remaining }}
- **Current landmark:** {{ args.current_landmark }}
- **Day on the trail:** {{ args.day }}

## Output contract

Write **2 to 4 sentences** of period-flavored prose describing the
moment of failure and the wagon master's quick assessment. Mention the
specific part, and let the spares-remaining number colour the tone
(zero spares → grim; plenty in reserve → matter-of-fact). Do **not**
prescribe actions — the player still picks repair / wait_out / quit.

Your final message is the prose narration and nothing else.
