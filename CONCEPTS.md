# Concepts

> Shared domain vocabulary for this project — entities, named processes, and status concepts with project-specific meaning. Seeded with core domain vocabulary, then accretes as ce-compound captures and refreshes learnings; direct edits are fine. Glossary only, not a spec or catch-all.

## Signoff cards

### Signoff card

A card with an Approve and a Cancel button that a program running under a session's agent posts into
that session's thread, so that only a person's click — never the agent and never typed text — can let
the project's signoff command run.

A card is open until it is resolved by one counted click, or replaced by a newer card in the same
thread. Either way it is drawn once more without buttons and with its mark. Open cards live in memory
only: after a restart, a click on an older card runs nothing.

### Mark

The footer a resolved or replaced signoff card is redrawn with: which way it ended, who clicked, and
when.

### Report turn

The turn the agent gets in a signoff card's session after every outcome of that card, telling it how
the card ended, so the agent can answer the thread. After a click, messages typed meanwhile wait
behind it.
