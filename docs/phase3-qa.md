# Phase 3 QA

Checked the installed plugin against local Kandev at `http://127.0.0.1:38430` on 2026-11-01. The pass below is what 0.3.0 did. The minimap bug is fixed in 0.3.1, which was packaged, installed, and rechecked on the same map.

Loopback was `http://127.0.0.1:54393` (`kv.loopback_port`, confirmed by `POST /api/plugins/kandev-plugin-mindmap/webhooks/session`). Calls used the `mml1` token from that webhook. Tokens are not copied here.

## Results

| # | Check | Result |
|---|---|---|
| 1 | Plugin health and version | **Pass** |
| 2 | Create a map, 3-level tree, read it back, second client sees an op | **Pass** |
| 3 | Suggest branches (utility agent), proposal appears, accept it | **Pass** |
| 4 | One short Cursor research run: status moves, a node is recorded, then stop | **Pass** |
| 5 | Compile | **Skipped live.** Hand-built docs and the zip **pass** |
| 6 | cloudflared share: 404 without the token, 200 with it, then the process is gone | **Pass** |
| 7 | Canvas at `/mindmap`, including Tab to add a child | **Pass** (after the minimap fix) |
| 8 | About 1,000 nodes: snapshot time and the layout bench | **Pass** |

## Evidence

### 1. Health and version

- `GET /api/plugins/kandev-plugin-mindmap` → **200**. Before the fix: `version` `0.3.0`, `status` `active`, `last_error` null, install path `~/.kandev/plugins/kandev-plugin-mindmap/0.3.0`. After install: `version` `0.3.1`, `status` `active`.
- `GET /api/plugins/kandev-plugin-mindmap/health` → **404**. The manifest leaves `endpoints.health` empty. Kandev treats a live plugin as healthy when its process answers the runtime ping, which is what `status: active` means. `GET /health` on Kandev itself is **200** (`status: ok`, `v0.98.0`) and is not the plugin.
- `POST /api/plugins/kandev-plugin-mindmap/webhooks/session` `{}` → **200**, `contract` 1, `base_url` `http://127.0.0.1:54393`.
- `GET http://127.0.0.1:54393/` → **404** with an empty body, which is the loopback server answering.

### 2. Map, tree, and a second client

- `POST /m/<token>/api/maps` `{"workspace_id":"63c0eb8e-dc24-4599-8d5c-7eb649024122","title":"Phase 3 QA map"}` → **200**. Map `map_00VZg4DnVWn81lx4X7ur18FZ3q`, root `nd_00VZg4DnVH1LD261QVBogYqzDy`, rev 1.
- `POST .../ops` with three `node.create` ops (idea → research → example) → **200**, message type `ack`.
- `GET .../snapshot` → **200**, rev 4, 4 nodes:

  | Title | Kind | Depth |
  |---|---|---|
  | Phase 3 QA map | topic | 0 |
  | QA branch | idea | 1 |
  | QA subbranch | research | 2 |
  | QA leaf | example | 3 |

- Second client was the sync long-poll. `GET .../sync?wait=0&client_id=qa-reader` → **200**, messages `welcome`, `snapshot`, cursor `c4.1`. A held `GET .../sync?cursor=c4.1&wait=15&client_id=qa-reader` returned **200** in 412 ms with an `ops` message whose node title was `Sync marker`, cursor `c5.2`. The writer was a different `client_id` (`qa-writer`).

### 3. Suggest

Profile was Pi GLM Flash (`269a5e04-410f-4688-b2f5-92cb3ff2aea1`), the workspace utility profile, so this did not use the Claude account.

- `POST .../suggest` `{"node_id":"<root>","count":3,"profile_id":"269a5e04-410f-4688-b2f5-92cb3ff2aea1"}` → **200** in 9117 ms.
- Job `run_00VZg4LpGErRFnVYGKXRyzRUjh`, status `done`, harness `pi`, template `suggest`, `task_id` empty (utility agent, not a Kandev task).
- Proposal `prp_00VZg4LpHvFzVu8KYTuvyLVknz`, status `ready`, 3 items: "Regression sweep on critical paths", "Sync marker drift risk", "What are Phase 3 exit criteria?".
- `POST /m/<token>/api/proposals/prp_00VZg4LpHvFzVu8KYTuvyLVknz/review` `{"accept_all":true}` → **200**, proposal `accepted`, every item `accepted`. Snapshot rev 14 included the three nodes tagged `suggested`.

### 4. One Cursor research run

Profile Cursor - Flash (`8104feb1-ec27-42ee-ab23-90c57a983f98`), template `research`, mode `apply`, budget `max_depth` 1, `max_nodes` 2, `max_subagents` 0, `max_minutes` 2. No swarm, explorer, or compile.

- `POST .../runs` → **200** in 18 ms. Run `run_00VZg4ZX3rFfbqso91w4zPO5LT`, task `25a3d6a0-544d-4125-8888-784e5a26141b`, session `7e6c2e9f-7161-482a-8c0a-0ff456b1a898`, harness `cursor`, transport `mcp`, initial status `queued`.
- Polling the snapshot: `queued` (last output "New session started") → `running` → last output `MCP: tool` → a new node **Cursor QA note** under QA branch, authored by that run.
- The run was still `running`, so it was stopped and a second run was not started. `POST .../runs/run_00VZg4ZX3rFfbqso91w4zPO5LT/stop` → **204**. Next snapshot: status `stopped`. `POST /api/v1/tasks/25a3d6a0-544d-4125-8888-784e5a26141b/archive` → **200** `{"success":true}`.

### 5. Compile

`POST .../compile` always starts real agents: one section run per top-level branch, then a synthesis run (`server/app/compile.go`, `server/web/compile.go`). There is no fake or tiny path that skips the launch, so the live compile was not called.

Doc nodes were created with `node.create` instead (root `index.md`, single doc `phase3-qa.md`, section `food/index.md`, sub-doc `food/cheese.md`).

- `GET .../docs` → **200**. Tree slug `phase3-qa`, with `phase3-qa.md` as the single doc and `food/index.md` → `food/cheese.md` under the index.
- `GET .../export.zip?node_id=<doc root>` → **200**, `Content-Type: application/zip`, 859 bytes. Zip entries:

  ```
  phase3-qa.md
  phase3-qa/index.md
  phase3-qa/food/index.md
  phase3-qa/food/cheese.md
  phase3-qa/assets/
  ```

### 6. Share

`pgrep -fl cloudflared` was empty before starting.

- `POST .../share` → **200** in 5232 ms, `status` `active`, provider `cloudflared`, host `grocery-exact-polar-meal.trycloudflare.com`. `pgrep` showed `/opt/homebrew/bin/cloudflared tunnel --url http://127.0.0.1:54393`.
- DNS for the hostname was empty on the first lookup and resolved on the next (`104.16.230.132`, `104.16.231.132`). While the tunnel warmed, `curl` of the origin returned **530**. Once it was up:
  - `curl https://<host>/` → **404**
  - `curl https://<host>/m/<share token>/` → **200**
  - `curl https://<host>/m/mms1.not-a-real-token/` → **404**
- `DELETE .../share` → **204**. `pgrep -fl cloudflared` was empty (exit 1). Nothing was left running.

An earlier share on `training-lite-forever-ballet.trycloudflare.com` never resolved in DNS. That tunnel was stopped the same way (`DELETE` **204**, `pgrep` empty) before the successful attempt.

### 7. Canvas

Headless Chrome opened `http://127.0.0.1:38430/mindmap`. The Mind Map nav item was current, the list included "Phase 3 QA map", and opening it set the URL to `/mindmap?map=map_00VZg4DnVWn81lx4X7ur18FZ3q`.

The canvas (`data-testid="mm-map"` / `mm-canvas`) rendered the tree, SVG edges, and the text "13 of 13 nodes rendered", including QA branch → QA subbranch → QA leaf, Cursor QA note, the doc nodes, and the accepted suggestions.

On 0.3.0 the minimap was an in-flow SVG about 1600×583 covering the top of the canvas (see Bugs). After installing 0.3.1 and reloading, `.mm-minimap` was `position: absolute`, **180×110**, at the bottom right, and `#mm-minimap-vp` was present. The black block was gone. Clicking the root and pressing Tab added a child titled "New idea" and opened its editor (14 nodes, snapshot rev 19). Enter committed the title.

### 8. Perf

Map `map_00VZg6WnWBgpz7ovl82Fl4R0Ko` ("Phase 3 perf 1000"), 1001 nodes (root + 1000 ideas), rev 1001.

A 200-op batch returned **200** in 45 ms and spent the burst. The next batch returned **429** `rate_limited` with `retry_ms` 3959, which matches the 50 ops/s sustained limit and burst of 200. The rest were sent in batches of 40 about once a second (all **200**, about 16–35 ms each).

Five snapshots: 6.98, 8.33, 8.80, 9.22, 9.60 ms. **Median 8.8 ms.**

`npx vitest run tests/layout.bench.test.ts` passed. Printed report for 1,000 nodes, all under the 16 ms budget:

```
fullMedianMs 1.266
incrementalMedianMs 0.689
incrementalMaxMs 0.987
displayMedianMs 0.770
```

## Bugs

**Minimap covered the canvas.** `.mm-minimap` had no CSS, so the overview SVG sat in normal flow at the canvas width. Its map rectangle used `fill="var(--bg-muted)"`, which is not a host colour, and painted as a large black block. That block also sat on top of nodes and took their clicks. The camera update in `Canvas.tsx` looks for `#mm-minimap-vp`, and the minimap never set that id.

Fixed in 0.3.1:

- `ui/plugin.css`: the minimap is a 180×110 absolute overlay.
- `ui/src/canvas/minimap.tsx`: fill is `var(--muted)`, and the viewport rect has `id="mm-minimap-vp"`.
- `tests/minimap.test.ts` renders the minimap and checks the stylesheet.

`npx vitest run tests/minimap.test.ts` passed. `make package-host` then `curl -F package=@kandev-plugin-mindmap-0.3.1.tar.gz http://127.0.0.1:38430/api/plugins/install` → **201**. The served stylesheet (`GET /api/plugins/kandev-plugin-mindmap/ui/ui/plugin.css` → **200**) contains the rule, and the live canvas measured 180×110.

No other product bug turned up. `GET /api/plugins/kandev-plugin-mindmap/conversation/v2/task-sessions/<id>/messages` returned **401** from curl with no Kandev session; the node and `last_output` were still visible on the snapshot, so this was not treated as a plugin defect.

## Left running

- No cloudflared process (`pgrep -fl cloudflared` empty).
- No second agent run. The Cursor research task `25a3d6a0-544d-4125-8888-784e5a26141b` was stopped and archived. The suggest run has no task id and is `done`.
