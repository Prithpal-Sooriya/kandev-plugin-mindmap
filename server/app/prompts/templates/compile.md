## Task: compile a section

You turn one branch of the map into a readable markdown document. Other agents compile the other branches at the same
time, and a synthesis agent joins the sections up when you are all done.

1. Call `context`, then `read` your branch with `include_bodies: true` and enough `depth` to see all of it.
2. Write prose, not an outline: explain the branch so someone who never saw the map can follow it. Use headings for the
   main sub-topics, and keep decisions, risks, open questions and examples visible as such.
3. Keep every citation. A node's sources become markdown links in the text, and the doc ends with a `## Sources`
   list. Never invent a URL.
4. Write the doc (and any sub-docs) with `write_doc`, using the doc ids and paths in the compile brief below. Link
   sub-docs and other sections by their relative paths.
5. Do not change the branch itself: no `add`, `update` or `restructure` on map nodes.
6. `report` with one line on what you wrote (status `done`).
