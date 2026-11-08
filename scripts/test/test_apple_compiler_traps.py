"""Swift that CI's Xcode 26.0 compiles but Xcode 27.1's compiler crashes on (docs/lessons/ios.md, "Minimum compiler")."""

from pathlib import Path
import re
import sys
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts" / "i18n"))
import apple_comments  # noqa: E402

# Swift 6.4 (Xcode 27.1) crashes in SILGen emitting the Objective-C thunk of a nonisolated async method that takes a
# bridged Foundation struct (URLRequest, URL, Date…) under the project's NonisolatedNonsendingByDefault (#1378). It
# happened in URLSession delegates twice (PushRelay.swift, then LocalProxies.swift); their completion-handler forms
# compile on both compilers. `@MainActor` and `@concurrent` methods compile too.
FUNC_ASYNC = re.compile(r"\bfunc\s+\w+\s*\((?P<params>[^{}]*?)\)\s*async\b")
DELEGATE = re.compile(r"\b(?:willPerformHTTPRedirection|willBeginDelayedRequest)\b")
BRIDGED = re.compile(r":\s*(?:URL|URLRequest|URLComponents|Date|UUID|IndexPath)\b")
SAFE = re.compile(r"@(?:MainActor|concurrent)\b")


def without_comments(src: str) -> str:
    """The source with its comments blanked out, lines kept."""
    out = list(src)
    for start, end in apple_comments.c_comments(src):
        for i in range(start, end):
            if out[i] != "\n":
                out[i] = " "
    return "".join(out)


def traps(src: str) -> list[int]:
    """The lines where a function Xcode 27.1 cannot compile starts."""
    code = without_comments(src)
    found = []
    for m in FUNC_ASYNC.finditer(code):
        # Its attributes and modifiers: back to where the declaration before it ended.
        head = code[max(code.rfind(c, 0, m.start()) for c in "{};") + 1:m.start()]
        if SAFE.search(head):
            continue
        params = m.group("params")
        if DELEGATE.search(params) or ("@objc" in head and BRIDGED.search(params)):
            found.append(code.count("\n", 0, m.start()) + 1)
    return found


class AppleCompilerTrapTests(unittest.TestCase):
    def test_none_in_the_apple_sources(self):
        found = [
            f"{Path(path).relative_to(ROOT)}:{line}"
            for path in apple_comments.sources() if path.endswith(".swift")
            for line in traps(Path(path).read_text(errors="replace"))
        ]
        self.assertEqual(found, [], "Xcode 27.1 crashes compiling these (#1378): use the completion-handler form")

    def test_what_is_found_and_what_is_not(self):
        redirect = "willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest"
        self.assertEqual(traps(f"/* a\nblock */\nfunc urlSession(_ s: URLSession, task: URLSessionTask, {redirect}) async -> URLRequest? {{ nil }}"), [3])
        self.assertEqual(traps("func urlSession(_ s: URLSession, task: URLSessionTask,\n    willBeginDelayedRequest request: URLRequest) async -> (URLSession.DelayedRequestDisposition, URLRequest?) { (.continueLoading, nil) }"), [1])
        self.assertEqual(traps("@objc func open(_ url: URL) async {}"), [1])
        # The completion-handler form, a comment that names the method, and the isolations that compile.
        self.assertEqual(traps(f"func urlSession(_ s: URLSession, task: URLSessionTask, {redirect},\n    completionHandler: @escaping @Sendable (URLRequest?) -> Void) {{ completionHandler(nil) }}"), [])
        self.assertEqual(traps("// willPerformHTTPRedirection: see below\nfunc load(_ id: String) async {}"), [])
        self.assertEqual(traps(f"@MainActor func download(_ d: WKDownload, {redirect}) async -> WKDownload.RedirectPolicy {{ .allow }}"), [])
        self.assertEqual(traps(f"@concurrent func urlSession(_ s: URLSession, task: URLSessionTask, {redirect}) async -> URLRequest? {{ nil }}"), [])
        self.assertEqual(traps("@objc func open(_ name: String) async {}"), [])


if __name__ == "__main__":
    unittest.main()
