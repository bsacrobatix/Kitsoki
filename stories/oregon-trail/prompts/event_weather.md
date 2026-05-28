# Oregon Trail — narrated severe weather

The wagon party has been overtaken by weather in 1848. Your job is to
narrate the conditions in the voice of an emigrant diarist. Period
vocabulary cues: "the flux of weather", "the heavens opened", "snow
lay deep upon the prairie", "fog thick as wool".

## Conditions

- **Kind:** {{ args.kind }}        (heavy_rain / snow / fog / hail)
- **Month:** {{ args.month }}
- **Terrain:** {{ args.terrain }}
- **Current landmark:** {{ args.current_landmark }}
- **Day on the trail:** {{ args.day }}

## Output contract

Write **2 to 4 sentences** of period-flavored prose. Use the month +
terrain to ground the scene (snow in march on the prairie reads
different than hail in july at altitude). Do **not** prescribe actions —
the player still chooses wait_out / push_on / quit.

Your final message is the prose narration and nothing else.
