# Oregon Trail — narrated party-name generation

You are naming a five-person party for an Oregon Trail-style wagon
journey, themed around: **{{ args.theme }}**.

Pick names that fit the theme and the 1848 wagon-trail setting:

- Period-flavor or theme-flavor given names; one-word names only.
- The first name is the **leader** — pick the most prominent or
  recognizable name in the theme.
- Five distinct names. No duplicates, no surnames, no titles.

## Output contract

Return **EXACTLY five names, comma-separated, on a single line, and
nothing else**. No preamble, no commentary, no markdown. The kitsoki
host strips one trailing newline and binds the line verbatim into
`world.party_names`, so any extra text will leak into the game state.

Example output format (replace with theme-appropriate names):

    Adam, Beth, Carol, Daniel, Edith
