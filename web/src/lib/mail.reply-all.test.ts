import { describe, expect, it } from "vitest";
import { badAddresses, replyAllDraft, replyDraft, sendBody, type MailMessage } from "./mail";

describe("Reply all recipient tables", () => {
  it.each([
    {
      name: "group recipients with cross-field casefold duplicates",
      message: { from: { email: " sender@example.com " }, to: [{ email: "ME@example.com" }, { email: "team@example.com" }, { email: "SENDER@example.com" }], cc: [{ email: "TEAM@example.com" }, { email: "copy@example.com" }, { email: "me@example.com" }] },
      to: "sender@example.com, team@example.com", cc: "copy@example.com",
    },
    {
      name: "self-sent mail uses its original recipients",
      message: { from: { email: "ME@example.com" }, to: [{ email: "me@example.com" }, { email: "team@example.com" }], cc: [{ email: "copy@example.com" }] },
      to: "team@example.com", cc: "copy@example.com",
    },
    {
      name: "Cc-only participants get a sendable To without adding self",
      message: { from: { email: "me@example.com" }, cc: [{ email: "ME@example.com" }, { email: "copy@example.com" }, { email: "other@example.com" }] },
      to: "copy@example.com", cc: "other@example.com",
    },
    { name: "self-only mail leaves recipients empty for deliberate editing", message: { from: { email: "me@example.com" }, to: [{ email: "ME@example.com" }] }, to: "", cc: "" },
    { name: "missing headers leave recipients empty", message: {}, to: "", cc: "" },
    {
      name: "missing sender and empty, name-only or malformed addresses are skipped",
      message: { from: { name: "Sender" }, to: [{}, { email: " " }, { email: "invalid" }, { name: "Name only" }, { email: "team@example.com" }], cc: [{ email: "a@example.com,b@example.com" }, { email: "a@example.com;b@example.com" }, { email: "a@example.com\r\nBcc: hidden@example.com" }] },
      to: "team@example.com", cc: "",
    },
    {
      name: "display-name delimiters and quotes cannot corrupt generated fields",
      message: { from: { name: 'Sender, "Example"', email: "sender@example.com" }, to: [{ name: "Team; Example", email: "team@example.com" }], cc: [{ name: "Copy\nExample", email: "copy@example.com" }] },
      to: "sender@example.com, team@example.com", cc: "copy@example.com",
    },
  ])("$name", ({ message, to, cc }) => {
    const draft = replyAllDraft("account", { uid: 1, ...message }, " Me@example.com ")!;
    expect(draft).toMatchObject({ accountID: "account", to, cc });
    expect(badAddresses(draft.to)).toEqual([]);
    expect(badAddresses(draft.cc)).toEqual([]);
  });

  it.each([undefined, "", " ", "invalid", "me@example.com,other@example.com", "me@example.com;other@example.com", "Name <me@example.com>"])("waits for a reliable receiving identity (%s)", (receiver) => {
    expect(replyAllDraft("account", { uid: 1, from: { email: "sender@example.com" }, to: [{ email: "me@example.com" }] }, receiver)).toBeNull();
  });

  it("inherits every ordinary reply field and never discovers hidden recipients", () => {
    const message: MailMessage = {
      uid: 2, subject: "RE: Review", date: "2026-10-28T06:05:00Z", text: Array.from({ length: 45 }, (_, i) => `Line ${i + 1}`).join("\n"),
      from: { email: "sender@example.com" }, to: [{ email: "me@example.com" }], message_id: "<message@example.com>", references: ["<parent@example.com>"],
    };
    const withHidden = { ...message, bcc: [{ email: "hidden@example.com" }] };
    const ordinary = replyDraft("account", message);
    const all = replyAllDraft("account", withHidden, "me@example.com")!;
    expect(all).toEqual({ ...ordinary, to: "sender@example.com", cc: "" });
    expect(all.body).toContain("> Line 40");
    expect(all.body).not.toContain("> Line 41");
    expect(JSON.stringify(sendBody(all))).not.toContain("hidden@example.com");
    expect(sendBody(all)).toMatchObject({ in_reply_to: "<message@example.com>", references: "<parent@example.com> <message@example.com>" });
  });
});
