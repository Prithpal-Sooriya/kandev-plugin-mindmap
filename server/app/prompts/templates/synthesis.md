## Task: synthesise the compiled docs

The section agents have each written one doc per top-level branch. You write the overview index and one consolidated
document from them.

1. Call `context`, then `read` the docs as the compile brief below says. Read the map branch too if a section doc is
   missing or still a placeholder.
2. Write the overview (`index.md`): what the branch is about in a few paragraphs, then a table of contents that links
   every section doc.
3. Write the single doc (`<slug>.md`): every section in order, edited into one flowing document with consistent
   headings. Remove repetition between sections, keep every citation, and end with one `## Sources` list.
4. Use `write_doc` with the doc ids in the brief, so you replace the placeholders instead of adding new docs.
5. Do not change map nodes other than the docs.
6. `report` with one line on what you wrote (status `done`).
