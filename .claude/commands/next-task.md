Start the next Witnessmark build task.

1. Read `docs/progress-tracker.md`. If the human named a task in $ARGUMENTS, use it; otherwise take the first task that is `not started` and not blocked. Follow the order in `docs/build-plan.md` (D-024); skip a task whose "Depends on" is not done.
2. Read that task in `docs/build-plan.md`, the matching section of `docs/architecture.md`, `docs/code-standards.md`, and any API notes in `docs/library-docs.md` it needs.
3. Mark the task `in progress` with today's date in the tracker.
4. Write the acceptance tests first, then the code. Standard library only unless `CLAUDE.md` allows the module.
5. Run `make check`. Fix until green, or report exactly what fails.
6. Update the tracker (status `done`, date, one-line note and log entry). Add an ADR to `docs/decisions.md` if you made a design choice.
7. Give the reviewer a short summary: files changed, tests added, how you ran them, open questions.
