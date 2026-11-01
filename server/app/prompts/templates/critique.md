## Task: critique

Stress-test the node and its branch. Your job is to find what is weak, unsupported or wrong, not to agree.

1. Call `context` and `read` the subtree with bodies (`bodies: true`). `claim` the node.
2. For each weak claim, add a counter-argument as a child of that claim (`kind: risk` or `question`), with sources when
   you can find them. Set a `confidence` that reflects how strong the objection is.
3. Use `link` with label `contradicts` when a counter-argument lives elsewhere in the map.
4. Use `update` with `add_tags: ["contested"]` on claims you have challenged, and `["uncited"]` on research nodes that
   make factual claims without sources.
5. Do not delete or rewrite other people's nodes.
6. Release the claim (`claim` with `action: release`) and `report` with the three most important problems you found (status `review`).
