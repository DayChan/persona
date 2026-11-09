#!/usr/bin/env python3
"""Loopback-only provenance fixture. No real conversations, accounts or credentials are used."""
import argparse
import copy
import html
import importlib.util
import json
import os
from pathlib import Path
import signal
import sys
from http.server import ThreadingHTTPServer
from urllib.parse import urlparse


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--port-file", type=Path, required=True)
    parser.add_argument("--timeout", type=int, default=1800)
    parser.add_argument("--legacy", action="store_true")
    parser.add_argument("--watch", action="store_true")
    args = parser.parse_args()
    os.environ["MOCK_TASKS"] = "1"
    spec = importlib.util.spec_from_file_location("source_fixture_base", Path(__file__).with_name("mock_hub.py"))
    base = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = base
    spec.loader.exec_module(base)
    conversation = base.ASK1
    base.TODOS = []
    base.TASK_FEED[conversation] = []
    base.ASSISTANT[:] = [copy.deepcopy(s) for s in base.ASSISTANT if s["session_id"] == conversation]
    base.ASSISTANT[0].update(title="消息来源预览", state="working" if args.watch else "idle", status="working" if args.watch else "idle", quiet=False)
    if args.watch:
        base.SESSIONS.clear()

    def relay(title, source_id, body):
        return (f'<persona-context kind="context" title="来自对话「{html.escape(title, quote=True)}」" url="">\n'
                f"这条消息不是用户本人发的，是 Persona 的另一段对话（{source_id}）用 conversation_send 发来的，第 1 跳（最多 4 跳）。\n"
                f"按里面说的做。要把结果告诉那段对话就用 conversation_send 发回 {source_id}；只是确认收到就不要回，免得两段对话来回发。\n"
                f"</persona-context>\n{body}")

    patrol_body = "巡检结果：两项验收等待确认。\n• 构建与测试已经通过。\n• 浅色和深色截图待复核。\n• 合并前仍需检查 CI。\n展开后可查看完整验收清单。"
    cross_body = "已核对开源发布清单，没有新增阻塞项。\n下一步：补齐支持文档后再发布。"
    messages = [base.it("user", "text", 1, text="请整理今天的巡检结果。", client_msg_id="fixture-user")]
    entries = [
        ("patrol", "supervision", "source-patrol", "督办巡检", patrol_body),
        ("patrol-next", "supervision", "source-patrol", "督办巡检", "补充：测试报告已归档，暂无新增问题。"),
        ("cross", "conversation", "source-review", "开源巡检", cross_body),
    ]
    for index, (item_id, kind, source_id, title, body) in enumerate(entries, 2):
        if kind == "supervision":
            text = ("📋 督办：你派出去的任务有 1 个要你处理\n"
                    "（这是 Persona 的督办消息，不是用户发的。这一轮默认折叠在主对话的督办记录中，不会推送给用户。）\n"
                    '<persona-supervision level="triage" />\n' + body)
            metadata = {"kind": "supervision"}
        else:
            text = relay(title, source_id, body)
            metadata = {"kind": kind, "conversation_id": source_id, "title": title}
        message = base.it("user", "text", index, text=text)
        message.update(id=item_id, seq=index)
        if not args.legacy:
            message["source"] = metadata
            encoded = json.dumps(metadata, ensure_ascii=False).replace("<", "\\u003c")
            message["text"] = f'<persona-context kind="message-source" title="">\n{encoded}\n</persona-context>\n{text}'
        messages.append(message)
    messages.append(base.it("assistant", "text", 5, text="收到，我会先复核截图，再按验收清单处理。"))
    for index, message in enumerate(messages, 1):
        message["seq"] = index
        message["ts"] = base.ago(minutes=(60 - index * 5) / 60)
    if args.watch:
        messages = messages[1:]
    base.ITEMS[conversation] = messages

    class Handler(base.H):
        def log_message(self, *_):
            base.LOG.write(f"{self.command} {urlparse(self.path).path}\n")
            base.LOG.flush()

        def do_GET(self):
            if urlparse(self.path).path == "/mock/sources/status":
                return self.send_json({"conversation": conversation, "legacy": args.legacy,
                                       "messages": len(messages), "sources": sum("source" in m for m in messages)})
            return super().do_GET()

    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    server.daemon_threads = True
    base.PORT = server.server_port
    base.ENDPOINTS.append(base.endpoint("127.0.0.1", server.server_port, "http", "lan"))
    args.port_file.write_text(str(server.server_port))

    def stop(*_):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGALRM, stop)
    signal.alarm(args.timeout)
    print(f"READY: source fixture at 127.0.0.1:{server.server_port}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("RESULT source fixture: stopped", flush=True)
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
