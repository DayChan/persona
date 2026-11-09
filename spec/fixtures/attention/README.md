# "Needs you" summary fixtures

Persona Link's "Needs you" summary (docs/operations/link.md "Needs you") lists and counts what the web app's "Needs you"
badge counts. This fixture holds both sides to it: the manager's summary (`linkAttention` in
`server/internal/assistant/fleet_attention.go`, `TestLinkAttentionFollowsTheSharedFixtures`) and the web app's own functions
(`web/src/lib/attention-fixture.test.ts`).

| Rule | Source | Cases |
| --- | --- | --- |
| The count is the badge's as it is: agents in "Needs you", without quiet tasks' agents and the assistant's conversations; muted agents and agents waiting only on a question the assistant is still deciding on (`delegating`) are counted. | `stateCounts().badge` in `web/src/lib/sessions.ts`, `useAgentSessions` | every case |
| One item per agent, by its oldest card handed to the user (not a question the assistant is still deciding on); while it has none, by its oldest card. Beside a handed card that cannot stand (an elicitation), and without any card (a terminal dialog), the agent is `screen`. Cards of the same time go by id. | `needsYou` in `server/internal/assistant/fleet_live.go`, `linkAttention` | `badge_rules`, `deciding_only`, `deciding_beside_elicitation` |
| Items are ordered by when the agent started waiting, newest first (its card's `created_at`, else the session's `state_since`, else `updated_at`); the same time goes by machine id, then session id. | `linkAttention` in `server/internal/assistant/fleet_attention.go` | `badge_rules` |
| `muted`: the session mute wins over `allowed`, which wins over the space mute. | `isMuted` in `web/src/lib/session-actions.ts`, `mutedAs` in `server/internal/assistant/fleet_mutes.go` | `badge_rules` |
| `notify`: not muted, and the agent has a card that is the user's (not a question the assistant is still deciding on): the phone's push rule. The web app's system notifications agree for agents with cards (`lookingOnly`, `isMuted`); an agent in a terminal dialog before its card (`s-dialog`) rings there at once, while the phone and Persona Link wait for the screen card made for it. | `lookingOnly` in `web/src/lib/attention.ts`, `isMuted`; `tell` in `server/internal/assistant/fleet_ix.go` | `badge_rules`, `deciding_only`, `deciding_beside_elicitation` |
| `route` is the web app's session path. | `routePath` in `web/src/lib/router.ts` | every case |
| Nothing needs you: the count is 0 and `items` is an empty list. | issue #2214 | `nothing_needs_you` |

- Every card in `input.interactions` is one the apps were told of, as the web app sees them; a card the manager has not
  decided on yet (the Go tests' own cases) never reaches the web app.
- Structure: `{"version": 1, "cases": [...]}`. `input.sessions` are sessions as the hub sends them to the apps (`state`
  already worked out), `input.interactions` the pending cards oldest first, `input.mutes` the hub's mutes (`spaces`,
  `sessions`, `allowed`). `expected` is the summary without its texts: `needs_you`, `more` and the items in order, each
  with `machine_id`, `session_id`, `kind`, `muted`, `notify`, `since`, `route` and `interaction_id` (missing when no card stands for the agent).
- The web test takes the count and the set of agents straight from `stateCounts().badge` over the sessions `useAgentSessions`
  keeps, and `notify` from the web app's system notification rule (`lookingOnly`, `isMuted`) for agents with a card; an
  agent without one must not notify (the push rule). It compares the set of items, not their order.
- The Go test compares the items in order and checks texts, the cap of 20 and `more` separately (`TestLinkAttentionCapsAndClips`).
- All data is made up.
- Run: `cd server && go test ./internal/assistant -run LinkAttention` and `cd web && npx vitest run src/lib/attention-fixture.test.ts`.
