# Agentic orchestration in Sparkwing pipelines

Status: idea captured 2026-11-02 (Korey). Decision 84: extract later; fast release is the proving ground.

Sparkwing should officially support pipelines that coordinate agents and people: durable state, rules over that state, and approvals where an AI approval and a human approval are different things.

## Pieces

1. Durable state keyed by a subject, not a run: "branch X's review record", "tree T passed gate G at engine E". Any later run can read it. First instance: tree-keyed gate results (fast release).
2. Typed approvals: every approval records its approver kind (human, agent with model and session, policy) and identity. Rules can require specific kinds ("P0/P1 cleared only by the same agent reviewer", "production promotion needs one human", "billing paths need an Opus review").
3. Server-side enforcement: rules and approval records live in the controller, bound to the repository's trust proof. A pipeline that enforced its own rules could be edited to skip them.
4. Agent sessions as resources: resumable reviewer sessions, with cost and history visible on the run page.

## Extraction trigger

Generalize when a second use needs the same pieces, for example human sign-off for production or agent-to-agent handoffs. Until then, fast release builds the smallest server-side versions it needs.
