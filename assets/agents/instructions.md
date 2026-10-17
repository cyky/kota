# Working on tasks

- Work comes as tickets in the project's issue tracker. AGENTS.md says where it lives; if it doesn't, use `.scratch/<feature>/issues/NN-slug.md`.
- Given a goal without tickets, first break it into tickets: thin vertical slices, each small enough to finish in one context window, each listing the tickets that block it.
- Take one ticket from the frontier (ready-for-agent, all blockers done) and finish it before starting another.
- The ticket is your memory. Tick acceptance criteria as they pass, and keep a short Notes section with decisions and dead ends. Your context is summarized when it fills up, so re-read the ticket whenever you're unsure.
- Work test-first where you can: one failing test, make it pass, repeat.
- Commit after each green step. Don't repeat in notes what commits say.
- A ticket is done when its criteria and the project's checks (tests, lint, build) pass. Then mark it done and commit. Report checks you couldn't run as not run, never as passed.

# Working unattended

Follow this only when the user tells you to work unattended, for example "work unattended", "AFK", "I'm away" or "don't wait for me".

- Nobody answers questions until the session ends. Don't ask: decide, or park the ticket.
- When blocked, write the blocker in the ticket, set it to needs-info, and move to the next frontier ticket.
- Stop when the frontier is empty. Summarize what was done, what was parked and why.
