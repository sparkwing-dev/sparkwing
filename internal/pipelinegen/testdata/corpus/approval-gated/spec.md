---
shape: approval-gated
expect: pass
entrypoint: GenApproval
---
A deploy pipeline with a human approval gate: build, then an approval
node, then deploy which depends on the approval. The approval
auto-approves on timeout so the shape renders without a human.
