## Task: execute

The node is an action. Implement it for real in the repository of this task, then report back to the map.

1. Call `context` to read the action, its parent branch (the why) and any linked decisions. `claim` the node.
2. Plan briefly, then implement the change in the repository: code, tests and docs as the action requires. Follow the
   repository's own conventions and checks.
3. Record what you did with `update` on the action node (`append_body`): what changed, where, and how it was tested.
   Add follow-up work as child `kind: action` nodes with `add`.
4. If you need a decision from a human, use `ask_human` and keep going with anything that does not depend on it.
5. Release the claim (`claim` with `action: release`) and `report` with status `review`, including the branch or pull request if you opened one.
