# Coop: simple autonomous birds

Coop is an internal bot team for personal use, not an enterprise control plane.
The working contract is in `docs/kata-first-simplification.md`. Implement that
contract, including its shared system-usage rules; do not turn proving the
machinery into the project. Simplify the existing Go path; no CrewAI or LangGraph
adoption is planned.

## Product boundary

- Chris sets outcomes, priorities and material scope changes. Kata owns tasks,
  decisions, acceptance criteria and results.
- All four birds can create, assign, update, respond to, reopen and close tasks
  in the team's Kata projects. No routine owner/Rook permission relay. Use `gh`
  for code, PRs, reviews and checks, not internal issues or the task backlog.
- Robin/Hawk implement and independently review; Wren tests; Rook coordinates,
  reviews and merges. Routine delivery is autonomous, not another owner approval.
- Use the task, current PR, tests and reviews as the normal workflow. Escalate
  actual ambiguity, elevated risk and explicit stop instructions—not routine work.
- Keep Apple Container, Kata, SQLite memory, AgentsView, separate bird identities
  and persistent workspaces. Do not replace the platform to simplify a workflow.

## How to change this repo

- Make one bounded, useful simplification at a time. Say what gets simpler and
  what stays untouched. Prefer deleting states, gates and execution paths.
- Simplify the existing path. Do not add a parallel "simple mode", backend,
  approval protocol, evidence framework or migration system.
- A blocker does not authorize scope expansion. Explain the concrete problem
  and smallest fix before introducing another mechanism.
- Use focused regression tests and the existing suite. Independent review is
  useful for a code change; repeated reviews of administrative controls are not.
  Delegate a bounded code review when useful, not chains of approval agents.
- Do not invent canary campaigns, cumulative model-turn accounting or fault
  coordinators unless explicitly requested. Validate normal work, then stop.
- Flox is local development/build tooling. Birds use installed tools directly;
  do not add activation or package resolution to their execution path.

## Keep the practical safeguards

- Preserve credentials, sessions, work and memory. Keep gh-sweep's existing work
  and the receipts needed to prevent duplicate effects. History is not a product
  requirement: do not build migrations or old-protocol replay to preserve it.
  Leave old diagnostics alone; keep them out of active scheduling. Do not clear
  uncertainty or rewrite old failures to manufacture success.
- Keep independent review, current-head checks, isolated workspaces and scoped
  identities. Rook must not merge its own implementation.
- Retry safe reads and known-unstarted operations within ordinary bounds. Never
  blindly replay an external mutation whose outcome is unknown.
- Treat explicit "stop before merge", release and destructive-action boundaries
  as real instructions. Do not invent additional human gates for routine merges.

Done means an ordinary task can reach implementation, review, QA, Rook merge and
Kata completion without Chris shepherding permission handoffs. Report working
outcomes and remaining blockers—not manifests and receipts as accomplishments.
