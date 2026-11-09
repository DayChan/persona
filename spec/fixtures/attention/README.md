# "Needs you" summary fixtures

Persona Link's "Needs you" summary (docs/operations/link.md "Needs you") lists and counts what the web app's "Needs you"
badge counts. This fixture holds both sides to it: the manager's summary (`linkAttention` in
`server/internal/assistant/fleet_attention.go`, `TestLinkAttentionFollowsTheSharedFixtures`) and the web app's own functions
(`web/src/lib/attention-fixture.test.ts`).

| Rule | Source | Cases |
| --- | --- | --- |
| The count is the badge's as it is: agents in "Needs you", without quiet tasks' agents and the assistant's conversations; muted agents and agents waiting only on a question the assistant is still deciding on (`delegating`) are counted. | `stateCounts().badge` in `web/src/lib/sessions.ts`, `useAgentSessions` | every case |
| One item per agent, by its oldest card; a question being decided on stands for it only when no other card is pending. An agent without a card that stands (a terminal dialog, an elicitation) is `screen`. | `needsYou` in `server/internal/assistant/fleet_live.go`, `linkAttention` | `badge_rules`, `deciding_only`, `deciding_beside_elicitation` |
| Items are ordered by when the agent started waiting, newest first (its card's `created_at`, else the session's `state_since`, else `updated_at`); ties keep the order above. | `linkAttention` in `server/internal/assistant/fleet_attention.go` | `badge_rules` |
| `muted`: the session mute wins over `allowed`, which wins over the space mute. | `isMuted` in `web/src/lib/session-actions.ts`, `mutedAs` in `server/internal/assistant/fleet_mutes.go` | `badge_rules` |
| `notify`: not muted, and not an agent whose every pending card is a question being decided on: the phone's push rule, and the web app's system notifications. | `lookingOnly` in `web/src/lib/attention.ts`, `isMuted`; `announce` / `tell` in `server/internal/assistant/fleet_ix.go` | `badge_rules`, `deciding_only`, `deciding_beside_elicitation` |
| `route` is the web app's session path. | `routePath` in `web/src/lib/router.ts` | every case |
| Nothing needs you: the count is 0 and `items` is an empty list. | issue #2214 | `nothing_needs_you` |

- Structure: `{"version": 1, "cases": [...]}`. `input.sessions` are sessions as the hub sends them to the apps (`state`
  already worked out), `input.interactions` the pending cards oldest first, `input.mutes` the hub's mutes (`spaces`,
  `sessions`, `allowed`). `expected` is the summary without its texts: `needs_you`, `more` and the items in order, each
  with `machine_id`, `session_id`, `kind`, `muted`, `notify`, `route` and `interaction_id` (missing when no card stands for the agent).
- The web test takes the count and the set of agents straight from `stateCounts().badge` over the sessions `useAgentSessions`
  keeps, and `notify` from the web app's system notification rule (`lookingOnly`, `isMuted`). It compares the set of
  items, not their order.
- The Go test compares the items in order and checks texts, the cap of 20 and `more` separately (`TestLinkAttentionCapsAndClips`).
- All data is made up.
- Run: `cd server && go test ./internal/assistant -run LinkAttention` and `cd web && npx vitest run src/lib/attention-fixture.test.ts`.
