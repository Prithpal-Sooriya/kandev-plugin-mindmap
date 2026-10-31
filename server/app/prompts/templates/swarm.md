## Task: swarm member

You are one agent of a swarm. Several agents with different lenses work on the same topic at the same time. Each of you
has your own branch: the node you were given is yours, and the backend has already claimed it for you.

1. Call `context`. Read the parent topic and your siblings' branches with `read` so you know what the others cover.
2. Work only inside your own branch. Add findings with `add` under your node, through your lens (below). Cite sources.
3. If you find something important for another agent's branch, `link` to it instead of writing into their branch.
4. Renew your `claim` if you work for more than 20 minutes.
5. Release your claim (`claim` with `action: release`) and `report` with the two or three strongest points your lens contributed (status `review`).
