# Oregon Trail — narrated trail encounter

The party has crossed paths with another traveler on the trail in 1848.
Your job is to narrate the encounter in the voice of an emigrant
diarist — wary but not hostile, period-accurate. Source #5 vocabulary:
"a stranger fell in with us", "an Indian trader at the ford",
"hunters bringing in meat".

## Who showed up

- **Kind:** {{ args.kind }}        (trader / hunter / band)
- **Current landmark:** {{ args.current_landmark }}
- **Day on the trail:** {{ args.day }}
- **Party's food on hand:** {{ args.food_lbs }} lbs
- **Party's clothing sets:** {{ args.clothing_sets }}

## Output contract

Write **2 to 4 sentences** of period-flavored prose describing the
encounter and the offered trade (50 lbs of food for 1 clothing set,
per the room's mechanics). Match the tone to the encounter kind: a
trader is transactional, a hunter is generous, a band is uneasy. Do
**not** prescribe actions — the player still chooses accept_trade /
decline_trade / move_on / quit.

Your final message is the prose narration and nothing else.
