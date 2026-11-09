# visionOS App

The native visionOS version of Persona (the `PersonaVision` target in `apple/`, #1687): it shares the logic layer (`Shared/`, `Persona/App`) and content views with the iOS App,
wrapped in a visionOS shell of its own (`apple/Vision/`). The approach follows the Mac version (`docs/apple/mac.md` ("Approach")): iOS and Mac do not change how they look for visionOS.

## Approach

- Compiled sources: `Vision/` + `Shared/` + `Persona/` (minus the iOS Info.plist and entitlements), plus the avatar images that AppModel reads from the Live Activity code
  (`LiveActivity/FleetPersona.swift`, `FleetIslandViews.swift`). The bundle id is the same as the iOS version; `TARGETED_DEVICE_FAMILY` is 7.
- iOS-only branches: where visionOS has the same API (UIKit views, WebKit, AVFoundation, PhotosUI…), widen `#if os(iOS)` to `#if os(iOS) || os(visionOS)`
  and use the iOS code as is. When new code adds `#if os(iOS)`, ask whether visionOS can take that branch too; if not, give visionOS a stand-in with the same name so PersonaVision still compiles.
- SwiftUI modifiers marked unavailable on visionOS (`scrollDismissesKeyboard`, `navigationSubtitle`, `scrollEdgeEffectHidden`, `GlassEffectContainer`,
  `sharedBackgroundVisibility`) get visionOS versions with the same names in `Vision/Compat/SwiftUICompat.swift`; pages are written as usual.
- `UIGestureRecognizerRepresentable` exists only on iOS: the message long-press gesture is rewritten as a same-named type using SwiftUI gestures (look plus long pinch) in `Vision/Media/MessageGestures.swift`.

## Mapping from iOS

| iOS | visionOS |
| --- | --- |
| Live Activities, Dynamic Island, Dynamic Island settings | None. `LiveActivityController` is replaced by a stand-in that only stores the state the hub pushes (`Vision/App/VisionStandIns.swift`), used by the "Needs You" ornament; settings do not show the Dynamic Island |
| Taking photos, scanning a QR code to pair | Vision Pro does not give Apps the camera: `CameraPicker.isAvailable` is false and the menu has no "Take Photo"; pairing uses a pasted link |
| Haptics | No-op |
| Drawers, swipe to switch | Pinch-and-drag scrolls on visionOS: the remote-screen drawer cannot be pulled down to close (tap the handle to close), and the chat's left/right paging switches only with buttons |
| VNC picture in picture | None (visionOS does not support sample-buffer picture in picture): `VNCPiP.supported` is false; only the full page opens |
| Liquid Glass | The window itself is glass: glass containers only do layout, surfaces use system materials |
| Share › Persona (`PersonaShare`) | The same code and Info.plist, compiled as the visionOS extension target `PersonaVisionShare` and embedded in the Vision App; same bundle id as iPhone (`<bundle id>.share`) |
| Opening relayed pushes (`PersonaNotify`, docs/operations/push-relay.md) | The same code and Info.plist, compiled as the visionOS extension target `PersonaVisionNotify` and embedded in the Vision App (#1981); same bundle id as iPhone (`<bundle id>.notify`), the `.push` keychain group shared with the app |
| Paste Image in the composer (`PasteImageChip`) | Same: visionOS has the system `PasteButton` and clipboard too |
| Voice Chat's call audio (`PhoneVoiceAudio`, docs/assistant/voice.md) | Same: `AVAudioSession` play-and-record / voice-chat, voice processing, the microphone permission and keeping the screen awake during a call are shared with iPhone; `VoicePlatform.current` is `.phone`. Not on the Mac yet |
| App icon | `AppIcon-Vision.solidimagestack` (written by `scripts/assets/make_icon.py vision`): the flat art split into two layers, the paper behind (a smooth fit of the border, opaque) and the drawing in front (the pixels that differ from the paper, with a feathered edge), the middle layer empty; visionOS wants at least two layers with content and masks the icon round |

## Shell

- The main window's tabs use the system `TabView`, which visionOS draws as a vertical ornament on the left of the window: this is the old iOS layout / iPad `RootTabs` (same pages, titles, `Theme.Icon` icons and badges),
  with the selection bound to `AppModel.selectedTab`. On visionOS, `RootView` does not build the iPhone's new shell (`ShellView`, `ShellState`), so there is no second source of truth:
  notifications, the Inbox and other places that write `selectedTab` switch tabs directly (the single source of truth described in `docs/apple/shell.md` is `selectedTab` here). The shell is `.visionShell()` in `Vision/App/VisionShell.swift`.
- A chat can open in its own window (`Vision/App/VisionScenes.swift`: `WindowGroup("对话", id: "chat", for: SessionKey.self)`, the same approach as the Mac's `MacChatWindow`);
  the session key is the window's restorable value, so the system restores these windows. Entry point: "Open in New Window" in the long-press menu of an Agents list row. The chat in the window is the first page (no back arrow); pages pushed from the chat live in the window's own navigation stack, as on iPad.
  DEBUG: `-openChatWindow <machine>/<session>` opens the window for that chat at launch (for screenshots).
- The "Needs You" ornament at the bottom of the main window (`.ornament(attachmentAnchor: .scene(.bottom))`): the count is the sum of the two tab badges (`AppModel.badges`: Persona's and Agents', muted Agents excluded);
  "Go" opens the one that has waited longest, or the next one if one of them is already open; hidden when the count is 0. The glass background is the system `glassBackgroundEffect`, wrapped in the design system's `.visionOrnamentPanel()` (`Vision/Design/VisionTokens.swift`, which also holds the visionOS-only size tokens).

## Build, run, screenshot

```bash
eval "$(python3 scripts/persona_config.py env)"
python3 scripts/persona_config.py xcconfig
(cd apple && xcodegen generate)
xcodebuild -project apple/Persona.xcodeproj -scheme PersonaVision \
  -destination 'generic/platform=visionOS Simulator' -derivedDataPath build/DD-vision \
  CODE_SIGN_IDENTITY=- CODE_SIGNING_ALLOWED=YES DEVELOPMENT_TEAM= build
xcrun simctl install <udid> build/DD-vision/Build/Products/Debug-xrsimulator/Persona.app
xcrun simctl launch <udid> <bundle id> -openWelcome YES -demo.on YES   # demo data from the demo mode
xcrun simctl io <udid> screenshot vision.png
```

- On the simulator, sign ad hoc locally (`CODE_SIGN_IDENTITY=-`): a build with `CODE_SIGNING_ALLOWED=NO` cannot read or write the keychain, so it cannot enter demo mode.
  Why: when the keychain read fails (missing entitlement), the registry counts as "unreadable" (`registryUnreadable`), and demo mode only opens when the registry is known to be empty.
  In DEBUG visionOS builds, `-openWelcome YES` treats the just-cleared registry as known empty, so demo mode works without signing too (not verified on the simulator).
- When you change code shared by iOS / Mac, build all three: `Persona`, `PersonaMac`, `PersonaVision`.
- On the visionOS simulator, `launch` after `simctl terminate` often shows no window (the process is there, the scene is not created): `simctl shutdown` and then `boot` first.
