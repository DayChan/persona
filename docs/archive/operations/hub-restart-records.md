# Hub restarts invisible to clients: records

Moved unchanged from the [hub restart guide](../../operations/hub-restart.md).
These are the measurements on the dev machine's hub after deploying socket activation and the resident node (#1617), with the journal of a restart before the change.

## Measured results (2026-11-04, this dev machine's hub, after deploying #1617)

Deployment: switched the hub to `personad.socket`, installed the resident node (`personad-node`); that initial switch stopped it for about 1 s. Afterwards:

| Scenario | Result |
| --- | --- |
| `hub_restart_seamless.py --restarts 3` (client only) | PASS: 247 health probes, 0 failures (slowest 0.95 s, waiting in the backlog); all 3 reconnects among 4 connections were `resumed` on the same stream, 54 events with no loss or duplicates; client-visible disconnects 0.73–1.01 s (web / Android grace is 8 s, 30 s during restart, so the UI stays unchanged); 1.3–1.6 s per restart including the command |
| `--restarts 3 --converse --count 300` (a streaming side chat plus a `sleep 75` background command) | PASS: 827 health probes, 0 failures; all 3 reconnects resumed, 121 events with no loss or duplicates, longest disconnect 0.81 s; all 300 answer lines present, the background command finished and reported back, no "just restarted" message in the chat; the node logged "took over 1 running conversations" three times (the same claude process) |
| Hub logs | Each restart: `event log: continuing stream … at N`, `warm start: 3 machines shown as before`, local node reconnects in 0.2–1 s, the two Macs over ssh in 1–3 s |
| Resident node idle restart (`node-launchd.sh restart-idle`) | PID unchanged (`exec`), the local node disconnected from the hub for about 2 s, which then broadcast an offline event; the 5 s grace and `dialResident` above were added afterwards (follow-up PR for #1574) |

Before-change comparison (journal of the deployment at 19:30 on 2026-11-04 on the same machine): the port closed for about 1.3 s and connections were refused; the local node and its assistant processes were killed; the two Macs reconnected after 2.4 s and 8 s, showing offline in between.
