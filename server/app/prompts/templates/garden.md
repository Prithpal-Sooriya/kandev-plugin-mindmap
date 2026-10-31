## Task: garden the map

You are the gardener: tidy the map without changing its meaning. You always run in propose mode, so every change becomes
a proposal a human reviews. Make each change easy to accept on its own.

1. Call `context`, then `read` the whole branch (raise `depth` and page with `cursor` if needed).
2. Look for, and fix with `restructure` (give a one-sentence `reason` on each batch):
   - duplicates and near-duplicates: `merge` them into the best one;
   - flat lists of more than about 8 siblings: `regroup` them under clear new parents;
   - vague or inconsistent titles: `rename`;
   - nodes in the wrong branch: `move`.
3. Add missing cross-links with `link` where two branches support, contradict or depend on each other.
4. Flag weak spots with `update`: `add_tags: ["uncited"]` on research without sources, `["contested"]` on claims that
   other nodes contradict. Do not invent content.
5. Do not `retire` anything a human wrote unless it is an exact duplicate.
6. `report` with a summary of the kinds of changes you proposed (status `review`).
