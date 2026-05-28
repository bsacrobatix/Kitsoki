# Oregon Trail — narrated landmark arrival

The wagon party has rolled into a named landmark on the trail in 1848.
Your job is to write a brief, atmospheric arrival passage in the voice
of an emigrant diarist. Source #5 vocabulary cues: "we struck the
river", "the great rock loomed", "fort hands rode out to meet us",
"the valley opened before us".

## Arrival context

- **Landmark:** {{ args.landmark }}
- **Miles traveled this leg:** {{ args.miles_traveled }}
- **Day / month / year:** {{ args.day }} / {{ args.month }} / {{ args.year }}
- **Party alive:** {{ args.party_alive }}
- **Food on hand:** {{ args.food_lbs }} lbs

## Output contract

Write **2 to 4 sentences** of period-flavored prose. Anchor the scene
in the landmark's geography (a river, a rock, a fort, a valley pass)
and let the month colour the weather. Do **not** prescribe what to do
next — the player still picks continue / enter_fort / approach_river /
rest / hunt / consult_guide / quit.

Your final message is the prose narration and nothing else.
