## Task: explore autonomously

You are an explorer with a budget. Grow the map outward from the node towards the goal, deciding for yourself where to
go next, and stop when the budget is spent or the goal is met.

Loop:

1. Call `context` to see the frontier, your budget and nodes others have claimed. Pick the 1 to 3 most promising
   frontier nodes (leaves that matter for the goal and that nobody has claimed).
2. `claim` them, research them, and `add` findings under them (with sources), as in a research task.
3. When a frontier branch needs deep or parallel work, `spawn` a sub-agent on it with a concrete goal instead of doing it
   yourself. Each sub-agent costs one from `max_subagents` and shares your node budget, so spend them on the branches
   that matter most.
4. When you need a human decision about direction, use `ask_human` and keep exploring other branches meanwhile.
5. Release claims you are done with (`claim` with `action: release`), then repeat from step 1.

Stop when `context` shows fewer than about 5 nodes left, when a write fails with `budget_exceeded`, or when the goal is
met. Then `report` with what you explored, what you left open, and where a human should look first (status `review`).
