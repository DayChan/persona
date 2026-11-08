"""git/review on the mock hub (#151, docs/web/review.md): the Review panel's answers for the made-up weather checkout
that MOCK_FILES serves (mock_hub.py MOCK_TREE), so the lines between hunks read from GET …/file are the files' own.

MOCK_REVIEW=1 turns on the hub feature git_review and the route; with MOCK_FILES=1 the panel can show hidden lines.
GET /mock/review?mode=<mode> switches what it answers and returns the requests seen so far (?reset=1 clears them):

  1          a branch with every kind of file: edits with hunks far apart, a new file, a generated lockfile, a picture,
             a rename, a removed file, one too big for a page (asked for on its own), a key it does not show, a file Git
             does not track yet
  clean      nothing changed
  nogit      the folder is not a repository (409 not_git)
  outdated   the computer's node is from before git.review (409 node_outdated)
  nodefault  no default branch: branch is 409 no_default_branch, the rest as 1
  slow       as 1, two seconds late

Everything here is made up.
"""
import difflib
import os
import time
import zlib

STATE = {"mode": os.environ.get("MOCK_REVIEW") or "1", "asked": []}

FORECAST_NEW = "\n".join(
    ["import Foundation", "", "/// The hours the widget shows, from the shared cache.", "struct Forecast: Codable {",
     "    var hourly: [Hour]", "    var updated: Date", "", "    struct Hour: Codable {", "        var time: Date",
     "        var temperature: Double", "        var precipitationChance: Double", "        var symbol: String", "    }", ""]
    + ["    // %s" % w for w in ("Parsing", "the", "feed", "is", "kept", "apart", "from", "the", "views", "so", "it", "can", "be", "tested", "alone.")]
    + ["", "    static func decode(_ data: Data) throws -> Forecast {", "        let decoder = JSONDecoder()",
       "        decoder.dateDecodingStrategy = .iso8601", "        return try decoder.decode(Forecast.self, from: data)", "    }", ""]
    + ["    func hour(at index: Int) -> Hour? {", "        hourly.indices.contains(index) ? hourly[index] : nil", "    }", ""]
    + ["    // %d" % i for i in range(1, 25)]
    + ["", "    /// The next hours, starting with the one under way.", "    func upcoming(from now: Date, count: Int = 6) -> [Hour] {",
       "        let start = hourly.firstIndex { $0.time.addingTimeInterval(3600) > now } ?? hourly.endIndex",
       "        return Array(hourly[start...].prefix(count))", "    }", "}", ""])
FORECAST_OLD = (FORECAST_NEW
                .replace("        var precipitationChance: Double\n", "        var rain: Double\n")
                .replace("    /// The next hours, starting with the one under way.\n    func upcoming(from now: Date, count: Int = 6) -> [Hour] {\n"
                         "        let start = hourly.firstIndex { $0.time.addingTimeInterval(3600) > now } ?? hourly.endIndex\n"
                         "        return Array(hourly[start...].prefix(count))\n",
                         "    func upcoming(from now: Date) -> [Hour] {\n        hourly.filter { $0.time > now }\n"))
MAIN_OLD = "import Foundation\n\nprint(\"Weather: sunny\")\n"
HOURLY_MD = "# Hourly forecast in the widget\n\n- The medium widget shows the next 6 hours.\n- Rain bars use the theme's rain color.\n- Refreshes every hour from the shared cache.\n"
LEGACY = "import Foundation\n\n// The old one-line forecast, before the widget.\nfunc summary() -> String {\n    \"sunny\"\n}\n"
RESOLVED = "{\n  \"pins\" : [\n" + ",\n".join(
    "    {\n      \"identity\" : \"%s\",\n      \"kind\" : \"remoteSourceControl\",\n      \"state\" : { \"version\" : \"%d.%d.0\" }\n    }" % (n, 1 + i, i * 3)
    for i, n in enumerate(("swift-algorithms", "swift-collections", "swift-numerics"))) + "\n  ],\n  \"version\" : 2\n}\n"
STATIONS = "[\n" + ",\n".join('  {"id": "ST-%03d", "lat": %.3f, "lon": %.3f}' % (i, 30 + i / 97, 120 + i / 89) for i in range(5200)) + "\n]\n"


def hunks(old, new, context=3):
    """The hunks git.review sends for two texts: a side's start is where its lines are (1-based), the header the
    nearest declaration above the hunk."""
    a, b = old.splitlines(), new.splitlines()
    out = []
    for group in difflib.SequenceMatcher(None, a, b, autojunk=False).get_grouped_opcodes(context):
        lines = []
        for tag, a1, a2, b1, b2 in group:
            if tag == "equal":
                lines += [" " + x for x in a[a1:a2]]
                continue
            lines += ["-" + x for x in a[a1:a2]]
            lines += ["+" + x for x in b[b1:b2]]
        i1, j1 = group[0][1], group[0][3]
        header = next((x.strip() for x in reversed(a[:i1]) if x.lstrip().startswith(("func ", "struct ", "static func "))), "")
        out.append({"header": header, "old_start": i1 + 1, "new_start": j1 + 1, "lines": lines})
    return out


def counts(hs):
    body = [l for h in hs for l in h["lines"]]
    return sum(l.startswith("+") for l in body), sum(l.startswith("-") for l in body)


def entry(path, old, new, status="modified", **kw):
    hs = hunks(old, new) if old != new else []
    added, deleted = counts(hs)
    return {"path": path, "status": status, "added": added, "deleted": deleted, "revision": "%07x" % (zlib.crc32(new.encode()) & 0xfffffff), "hunks": hs, **kw}


def files(tree, repo, edit_old, edit_new, scope):
    """The made-up files of a review of the weather checkout, in path order."""
    get = lambda rel: tree.get(repo + "/" + rel) or ""
    provider = get("WeatherWidget/Provider.swift")
    out = [
        entry("Package.resolved", RESOLVED.replace("3.6.0", "3.4.0"), RESOLVED, generated=True),
        entry("README.md", get("README.md").replace("A small sample app: 当前温度、逐小时预报和主屏幕小组件。\n\n", ""), get("README.md")),
        entry("Resources/stations.json", "", STATIONS, status="added", big=True),
        entry("Sources/App/Forecast.swift", FORECAST_OLD, FORECAST_NEW),
        {"path": "Sources/App/Legacy.swift", "status": "deleted", "added": 0, "deleted": 6, "hunks": hunks(LEGACY, "")},
        {"path": "Sources/App/Units.swift", "old_path": "Sources/App/Helpers.swift", "status": "renamed", "added": 0, "deleted": 0},
        entry("Sources/App/main.swift", MAIN_OLD, get("Sources/App/main.swift")),
        entry("WeatherWidget/Provider.swift", provider.replace(edit_new, edit_old), provider),
        entry("WeatherWidget/WidgetView.swift", "", get("WeatherWidget/WidgetView.swift"), status="added"),
        {"path": "deploy/id_ed25519", "status": "modified", "added": 0, "deleted": 0, "refused": True},
        {"path": "docs/hourly.md", "status": "added", "untracked": True, "added": 4, "deleted": 0, "hunks": hunks("", HOURLY_MD)},
        {"path": "docs/icon.png", "status": "added", "added": 0, "deleted": 0, "binary": True},
    ]
    for f in out:
        if f.get("big"):
            f.pop("hunks", None)
            f["added"] = STATIONS.count("\n")
    if scope == "uncommitted":
        keep = ("WeatherWidget/Provider.swift", "Sources/App/Forecast.swift", "docs/hourly.md", "deploy/id_ed25519")
        out = [f for f in out if f["path"] in keep]
    return out


def answer(q, root, tree, repo, commits, edit_old, edit_new):
    """(json, status) for GET …/git/review."""
    arg = lambda k: (q.get(k) or [""])[0]
    scope, base, paths = arg("scope"), arg("base"), q.get("path") or []
    STATE["asked"].append({"scope": scope, "base": base, "paths": paths})
    mode = STATE["mode"]
    if mode == "slow":
        time.sleep(2)
    if mode == "nogit":
        return {"error": "not a Git repository", "code": "not_git"}, 409
    if mode == "outdated":
        return {"error": "personad on this computer is too old to review changes. Update it", "code": "node_outdated"}, 409
    if scope not in ("uncommitted", "branch", "commit") or (scope == "commit" and not base):
        return {"error": "scope is uncommitted, branch or commit", "code": "bad_scope"}, 409
    head = commits[0]
    res = {"root": root, "scope": scope, "head": {"hash": head["hash"], "short": head["short"], "branch": "widget-hourly"}, "files": []}
    pick = lambda c, **kw: {"hash": c["hash"], "short": c["short"], "subject": c["subject"], **kw}
    if scope == "branch":
        if mode == "nodefault":
            return {"error": "no default branch (origin/HEAD, main or master)", "code": "no_default_branch"}, 409
        res["base"] = pick(commits[2], ref="origin/main")
    elif scope == "commit":
        c = next((c for c in commits if c["hash"] == base or c["short"] == base), None)
        if not c:
            return {"error": "not a commit of this repository: " + base, "code": "bad_base"}, 409
        res["base"] = pick(c)
    else:
        res["base"] = pick(head)
    if mode == "clean":
        return res, 200
    list_ = files(tree, repo, edit_old, edit_new, "uncommitted" if scope == "commit" and base in (head["hash"], head["short"]) else scope)
    if paths:
        list_ = [f for f in list_ if f["path"] in paths]
        for f in list_:
            if f.get("big"):  # asked for on its own: it comes
                f.pop("big")
                f["hunks"] = hunks("", STATIONS)
    res["files"] = list_
    return res, 200


def control(q):
    """GET /mock/review?mode=…&reset=1."""
    if q.get("mode"):
        STATE["mode"] = q["mode"][0]
    if q.get("reset"):
        STATE["asked"] = []
    return {"mode": STATE["mode"], "asked": STATE["asked"]}
