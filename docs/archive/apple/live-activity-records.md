# Lock Screen / Dynamic Island Live Activity: records

Moved unchanged from the [Live Activity guide](../../apple/live-activity.md), which now describes only the current design.
These are the build 92 investigation (the avatar ignored Settings) and the push measurements from the battery investigation that set the current push pacing (2026-11-02).

## Why build 92 didn't follow the setting (2026-11-02)

The user reported "the Dynamic Island avatar doesn't follow Settings". The screenshot showed the compact view with only Persona's activity (the "1.7B" on the right was Persona's own Tokens Today, not another app), and the left was the default avatar + system blue: what the extension draws when it can't read the keychain.

- **Root cause**: the TestFlight workflow archives unsigned (`CODE_SIGNING_ALLOWED=NO`), then signs ad hoc and uses cloud signing at export. In an unsigned build, `$(AppIdentifierPrefix)` is empty,
  and ad hoc signing didn't give the extension its entitlements: the widget extension installed on the phone had no keychain groups at all, the App's group name was missing its bundle id, and the group name in Info.plist was missing the team prefix.
  The App couldn't write to the shared group (missing entitlement, silently abandoned as if it were a free team), so the extension always drew the default avatar. The push relay's `.push` group was broken too (the notification extension couldn't decrypt pushes and showed only fallback text).
- **The other half**: #219 intentionally drew hub avatar packs as the default avatar in a different color, so even with the keychain working, selecting a pack wouldn't show its images.
- **Fix**: use `$(DEVELOPMENT_TEAM).<bundle id>.…` for group names in Info.plist (has a value even unsigned); `scripts/release/entitlements.py sign` gives each bundle in the unsigned archive
  its own entitlements (expands build variables and errors if any can't be expanded) before ad hoc signing. `scripts/release/entitlements.py check` checks each bundle's keychain groups and Info.plist group names in an ipa / archive;
  `release.sh` runs it after archiving, and the TestFlight workflow must also run it after export (it reported 9 problems in build 92's ipa). Avatar packs draw their own images (previous item).

## Push pacing: measurements (2026-11-02, battery investigation)

On the user's real hub, Dynamic Island content changed about 180 times per hour. Previously each change sent a push, and each received push redrew both the Dynamic Island and the Lock Screen. Now:

- Replay (19 recorded minutes, App closed): 188 pushes/hour → 115 (30 seconds); at 60 seconds, 60 pushes. App open: 0.
