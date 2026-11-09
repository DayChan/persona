# Build and install Persona Link on Mac

Persona Link is a small Mac menu-bar app. It connects this Mac to your Persona hub, letting the hub use this computer to view and drive Claude Code, Codex, and other Agents running here.
It bundles the persona-link connector and persona-node (the device's own program), and installs a persistent local node service on first launch. See [link.md](link.md) for the architecture, two connection modes, and protocol; this document covers installation only.

There is no published installer yet. Build from source for now; it takes a few minutes.

On a computer you use Persona through the web app; this app is the part that runs on the Mac ([link.md](link.md), "On computers: the web app and Persona Link").
If this Mac already runs the command-line Link (`persona-link run`), follow [link.md](link.md), "Move a Mac from the command-line Link to the menu bar App", instead: it stops that Link first and keeps its pairing and data directory.

## Prerequisites

| Requirement | Details |
| --- | --- |
| macOS 15 or later | The app's minimum OS version |
| Xcode (the full app, not just command-line tools) | Builds the Swift package with `xcodebuild`; after installation, run `sudo xcode-select -s /Applications/Xcode.app` and open Xcode once to accept its license |
| Go | Builds persona-link and persona-node. Use the version in `server/go.mod`; a slightly older local Go is fine if `GOTOOLCHAIN=local` is not set, since Go downloads the required version automatically |
| A running hub | And a browser already connected to it (the web client), to add this Mac. A hub set up with the install command already has the secure connection required for computer access; for an older hub, see "Advanced" under "Pairing" below |

No Apple developer account is needed: the app uses local ad-hoc signing and runs only on the Mac that built it.

## Build and install

```bash
git clone <repository-url> persona && cd persona
./scripts/app/mac-link.sh install
```

This command builds the app for the local architecture (Apple silicon / Intel), builds and bundles persona-link and persona-node, signs the app, installs it at `/Applications/Persona Link.app` (the filename remains Persona Link for compatibility with older versions; the app displays Persona Link), and opens it.
If an older version is running, it quits and replaces it first.

- Build without installing: `./scripts/app/mac-link.sh build`; output is `apple/PersonaLink/build/Persona Link.app`.
- Build for both Apple silicon and Intel: `UNIVERSAL=1 ./scripts/app/mac-link.sh build`.
- The default bundle ID is the placeholder `com.example.persona.link`; no need to change it for personal use. To change it, set `mac.link_bundle_id` in `~/.config/persona/config.json`
  (or the `PERSONA_LINK_BUNDLE_ID` environment variable). Changing the bundle ID makes it a different app, so enable "Launch at login" again.

After launch, an icon appears at the top right in the menu bar, with no Dock icon. On first launch:

- When the `com.persona.node` LaunchAgent is not installed yet, it copies the bundled persona-node to `~/.persona/bin/persona-node` and installs the agent to keep the local node running on it (on a Mac that runs a hub or a manager, the agent runs their personad). An agent that is already there stays as it is: one that runs personad moves over with `persona-link node switch`, [link.md](link.md) "The device's program". **Reinstall** in the menu (offered when the agent is installed but not running) removes the agent and installs it again, so it comes back on persona-node, without the switch's wait for an answer.
- On the first connection to a LAN address, the system asks whether to allow Persona Link to find devices on the local network. Choose Allow. If you denied it, enable it in **System Settings › Privacy & Security › Local Network**.

## Pairing

Add this Mac in the web client already connected to your hub, get a pairing link, paste it into Persona Link, then return to the web client to confirm that it is your computer.

1. Open **Machines** in the web client, click **Add computer**, choose Mac, give it a name (Chinese is supported too, such as "我的 MacBook"—"My MacBook"), and click Next. **Add computer** on iPhone and Android is still in progress; use the web client for now.
2. A pairing link appears. Click **Copy pairing link** or **Share…** and send it to this Mac (Notes, AirDrop, or email all work).
3. On this Mac, open the link directly: Persona Link opens its pairing window with the link filled in. Or click the Persona Link menu-bar icon › **Paste pairing link…**, keep the default **I connect to hub (active)**, and paste the link.
   Check that the hub shown in the window (**Hub to connect to**) is yours, then click Pair: opening a link never pairs by itself.
   The app is not signed or notarized: open it the first time with right-click › Open in Finder. Clicking a link works only after the app has been opened once.
4. Return to that web screen. It asks, "A computer wants to join: <name>. Is it yours?" Click **It's my computer**.
   Before confirmation, this Mac cannot receive Persona-managed model keys, and Persona will not run work on it. It is deleted automatically if not confirmed within 24 hours. If you closed that screen, you can also confirm on the computer's card in the machine list.
5. Success: the icon shows connected; the menu says "Local node online" and "This computer is '<name>' on the hub"; this computer is online in the web and mobile machine lists.
6. To connect at startup, enable **Launch at login** in the menu.

The link is valid for 10 minutes and can be used only once. If it expires, click **Regenerate** on that web screen. Until used, it grants pairing permission: send it only to your own computer, never post it in a group.

If the web client says "Secure connection isn't enabled on the hub yet", the older hub lacks the secure connection needed for computer access. Enable it in the hub's **Admin › Network**, or see "Advanced" below.

### Advanced

- **Get a link without the web client**: in the hub's **Admin › Computers › Add computer › Computer connects**, enter this computer's ID (such as `macbook`) and name; or run
  `personad hub pair --machine macbook --add --name MacBook` on the hub machine (inside the container for a Docker hub; see [docker.md](docker.md)). Computers added by either method are confirmed immediately; no need to click "It's my computer".
- **The hub must have TLS enabled**: active mode uses only TLS (`public.tls_listen` in hub.json), or a hub behind an HTTPS reverse proxy ([link.md](link.md), "Hub behind a reverse proxy"). A newly installed hub from `personad hub setup` enables it only on LAN IPv4 by default.
- **Let the hub connect (passive)**: when this computer cannot reach the hub but the hub can reach it (for example, the hub is on someone else's network and can only be reached through a proxy), select this option in the pairing window,
  click **Generate pairing link**, and paste it into **Admin › Computers › Add computer › Hub connects**. Allow its listening port (default 18663) through the firewall only for the hub. See [link.md](link.md), "Passive mode".
- **Pair in the terminal**:

  ```bash
  echo '<pairing-link>' | "/Applications/Persona Link.app/Contents/Helpers/persona-link" pair -
  ```

## Menu entries

| Entry | Meaning |
| --- | --- |
| LAN / Public IPv6 / Domain · address | Which route is currently used to connect to the hub |
| Hub "…" · N hubs in the linked group | Which hub is connected (absent when an older hub sends no name) |
| Local node online | Everything is working |
| Link connected, but the hub cannot reach the local node | The persistent node is not running; see "Troubleshooting" below |
| Disconnected · Retry in N seconds | One line per candidate address explaining why it failed; retries automatically, or click **Reconnect now** |
| Hub rejected this computer's credentials | The computer was removed from the hub or its credentials were revoked: pair again |

See [link.md](link.md), "Menu-bar app", for the icon states.

## Upgrade

Pull the latest code and install again. Pairing and settings live in `~/.persona/link.json` and are preserved:

```bash
git pull && ./scripts/app/mac-link.sh install
```

Upgrading the app does not replace the program the persistent node runs (persona-node, or personad on a Mac that had it before). A node that supports the update protocol can be updated through **Redeploy** on Persona's machine page, using the existing connection without reinstalling the menu-bar app.

Older nodes need one initial local upgrade. First inspect the actual service's binary path and status:

```bash
"/Applications/Persona Link.app/Contents/Helpers/persona-link" node status --json
```

Replace the service binary pointed to by `bin` through its original installation / deployment channel and restart `com.persona.node`, preserving the existing runtime root, launch arguments, and pairing. Do not uninstall before querying the path, and do not assume upgrading the app upgrades the persistent node. See [node-update.md](node-update.md) for the full initial migration and failure recovery procedure.

## Uninstall

1. If you no longer want the persistent node, stop it and remove its service first (before deleting the app): `"/Applications/Persona Link.app/Contents/Helpers/persona-link" node uninstall`.
2. Menu › **Quit Persona Link**; remove it from **System Settings › General › Login Items** if listed.
3. Delete the app: `rm -rf "/Applications/Persona Link.app"`.
4. Pairing credentials are in `~/.persona/link.json`, state in `~/.persona/link.status.json`, passive-mode TLS identity in `~/.persona/link-tls/`, logs in `~/.persona/link.log`, and the persistent node's program in `~/.persona/bin/persona-node` (or `~/.persona/bin/personad` on a Mac that had it before; if this computer has also run the install script, `~/.persona/bin/` contains `persona-link` too). Delete these if no longer needed. Remove this computer in the hub's **Admin › Computers**.

## Troubleshooting

**Build fails with `xcodebuild: error: SDK … cannot be located` or a missing developer directory**: the system points at command-line tools, not Xcode.
Run `sudo xcode-select -s /Applications/Xcode.app`, or temporarily `export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer`.

**Build says Go is too old, or toolchain download fails**: install Go at least as new as the version in `server/go.mod`, or allow `go` network access to download it automatically.

**The system says it cannot verify the developer when opening the app**: this happens to apps copied from elsewhere (ad-hoc signatures are trusted only on the machine that built them). Build on the Mac where you will use it.
If you must copy it, right-click › Open, or run `xattr -dr com.apple.quarantine "/Applications/Persona Link.app"`.

**Always "Disconnected", with the LAN route shown as unreachable**: local network permission was probably denied; see "On first launch" above. When away from home, LAN being unreachable is normal; it uses public IPv6 or a domain instead.

**"Link connected, but the hub cannot reach the local node"**: run `"/Applications/Persona Link.app/Contents/Helpers/persona-link" node status` to check whether the persistent node service is installed
and loaded by launchd. Logs are in `~/.persona/node-attach.log`. If not installed, click **Install local node service** in the menu.

**The menu-bar panel has the old square corners and thin border**: the app was built with an older script. Pull the latest code and run `install` again (see [lessons/macos.md](../lessons/macos.md), "Persona Link panel has the old appearance", for why).

**Read logs**: `tail -f ~/.persona/link.log`, or `"/Applications/Persona Link.app/Contents/Helpers/persona-link" logs -f`. On a computer that used the older Persona version, these files may still live under `~/.persona/` ([config.md](config.md), "Naming and compatibility").
