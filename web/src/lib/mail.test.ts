import { beforeEach, describe, expect, it, vi } from "vitest";

const calls = vi.hoisted(() => ({ list: [] as { path: string; init?: { json?: unknown } }[], fail: false }));
vi.mock("./api", () => ({
  api: vi.fn(async (path: string, init?: { json?: unknown }) => {
    calls.list.push({ path, init });
    if (calls.fail) throw new Error("boom");
    return path === "/mail/accounts" ? { accounts: [{ id: "acc1", email: "me@example.com" }] } : { ok: true };
  }),
  enc: encodeURIComponent,
}));

import { badAddresses, loadMailAccounts, markMailSeen, newDraft, parseAddresses, replyDraft, resetMailForTests, sendBody, sendMail } from "./mail";

beforeEach(() => {
  calls.list = [];
  calls.fail = false;
  resetMailForTests();
});

describe("addresses", () => {
  it("splits on commas, semicolons and new lines and keeps names", () => {
    expect(parseAddresses("a@example.com， b@example.com; 示例 <c@example.com>\n\"Lucy Wang\" <lucy@example.com>,")).toEqual([
      { email: "a@example.com" },
      { email: "b@example.com" },
      { name: "示例", email: "c@example.com" },
      { name: "Lucy Wang", email: "lucy@example.com" },
    ]);
    expect(parseAddresses("  ")).toEqual([]);
  });

  it("names the ones that are not an address", () => {
    expect(badAddresses("a@example.com, lucy, b@example")).toEqual(["lucy", "b@example"]);
    expect(badAddresses("示例 <c@example.com>")).toEqual([]);
  });
});

describe("replyDraft", () => {
  const msg = {
    uid: 501,
    message_id: "<m501@example.com>",
    references: ["<m400@example.com>"],
    subject: "下周的分享",
    from: { name: "Lucy Wang", email: "lucy@example.com" },
    date: new Date(2026, 9, 28, 14, 5).toISOString(),
    text: Array.from({ length: 45 }, (_, i) => `line ${i + 1}`).join("\n"),
  };

  it("answers the sender with Re:, the thread ids and the start of the mail quoted", () => {
    const d = replyDraft("acc1", msg);
    expect(d).toMatchObject({ accountID: "acc1", to: "lucy@example.com", cc: "", bcc: "", subject: "Re: 下周的分享", inReplyTo: "<m501@example.com>" });
    expect(d.references).toEqual(["<m400@example.com>", "<m501@example.com>"]);
    expect(d.body.startsWith("\n\n2026-10-28 14:05，Lucy Wang <lucy@example.com> 写道：\n> line 1\n")).toBe(true);
    expect(d.body).toContain("> line 40");
    expect(d.body).not.toContain("> line 41");
  });

  it("does not stack Re:", () => {
    expect(replyDraft("acc1", { ...msg, subject: "RE: 下周的分享" }).subject).toBe("RE: 下周的分享");
  });
});

describe("sending", () => {
  it("sends References as one string, the way the hub reads it", () => {
    const d = { ...newDraft("acc1"), to: "a@example.com", cc: "b@example.com", bcc: "Private <private@example.com>； second@example.com\nthird@example.com", subject: "s", body: "b", inReplyTo: "<x@y>", references: ["<w@y>", "<x@y>"] };
    expect(sendBody(d)).toEqual({
      account_id: "acc1",
      to: [{ email: "a@example.com" }],
      cc: [{ email: "b@example.com" }],
      bcc: [{ name: "Private", email: "private@example.com" }, { email: "second@example.com" }, { email: "third@example.com" }],
      subject: "s",
      text: "b",
      in_reply_to: "<x@y>",
      references: "<w@y> <x@y>",
    });
    expect(sendBody({ ...newDraft("acc1"), to: "a@example.com" })).not.toHaveProperty("references");
  });

  it("starts new mail with all recipient fields empty", () => {
    expect(newDraft("acc1")).toEqual({ accountID: "acc1", to: "", cc: "", bcc: "", subject: "", body: "" });
  });

  it.each(["cc", "bcc"] as const)("serializes %s-only mail with empty other recipient lists", (field) => {
    expect(sendBody({ ...newDraft("acc1"), [field]: "Only <only@example.com>" })).toEqual({
      account_id: "acc1", to: [], cc: [], bcc: [], subject: "", text: "",
      [field]: [{ name: "Only", email: "only@example.com" }],
    });
  });

  it("posts Bcc recipients to /mail/send", async () => {
    await sendMail({ ...newDraft("acc1"), bcc: "private@example.com", subject: "hi", body: "hello" });
    expect(calls.list[0]).toEqual({ path: "/mail/send", init: { method: "POST", json: { account_id: "acc1", to: [], cc: [], bcc: [{ email: "private@example.com" }], subject: "hi", text: "hello" }, headers: undefined } });
  });

  it("marks a mail read and swallows a failure", async () => {
    await markMailSeen("acc 1", 501, "INBOX");
    expect(calls.list[0]).toEqual({ path: "/mail/messages/acc%201/501/flags", init: { method: "POST", json: { mailbox: "INBOX", seen: true } } });
    calls.fail = true;
    await expect(markMailSeen("acc1", 502, "INBOX")).resolves.toBeUndefined();
  });

  it("shares one accounts request while it is out", async () => {
    const [a, b] = await Promise.all([loadMailAccounts(), loadMailAccounts()]);
    expect(a).toEqual([{ id: "acc1", email: "me@example.com" }]);
    expect(b).toBe(a);
    expect(calls.list).toHaveLength(1);
  });
});
