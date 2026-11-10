# Comment blocks at the start of a message

The `> ` blocks a message starts with when it carries comments: on picked words (`Comment N`), on lines of a file or a diff (`Code comment N`, a review's comments, #151) and on an element of a dev server's page (`Element comment N`, #150). The format is in [protocol.md](../../../docs/protocol/protocol.md), "Quote and comment blocks at the start of messages". The web app (`web/src/lib/comments.ts`, `web/src/lib/drafts.ts`) and the iOS app (`apple/Persona/Views/Chat/MessageComments.swift`) both write and read these blocks and check them against these cases.

## Rules

- A block is the comment's lines with `> ` in front of each; a blank line inside a block is `> ` with its space. Blocks are separated by a blank line, and the typed text follows after one more blank line (nothing follows when nothing was typed). `write_review_with_summary`
- Comments are numbered from 1 in the order they wait above the input. `write_review_with_summary`, `write_note_trimmed`
- A code comment is its number, `File: <path>:<line>` or `:<from>-<to>`, then `Side: old` when only removed lines were picked, `Diff: <scope>` for a diff, `Truncated: the first <shown> of <all> lines` when lines were left out, the code in a fence with its language, a blank line and the note. `write_code_comment`, `write_old_side_diff_truncated`
- `Diff:` is `staged`, `unstaged`, `commit <first 8 hex>`, or a review's `uncommitted`, `branch since <hash>` or `since <hash>`. `write_old_side_diff_truncated`, `write_review_with_summary`
- The fence has one more backtick than the longest run in the code, and at least three. `write_fence_longer_than_code`
- A comment on picked words is its number, `Selected text:`, the words each as `> <line>`, a blank line and the note. `write_selected_text`
- An element comment is its number, `Page:`, `Element:`, then `Component: <name> (<source>)` or `Source: <source>`, `Text:` with every run of whitespace as one space, `Truncated:` when HTML lines were left out, the HTML in an `html` fence, a blank line and the note. `write_element`, `write_element_source_truncated`
- The note is trimmed; with no note there is no blank line after the fence or the words. `write_fence_longer_than_code`, `write_note_trimmed`
- Reading back: the first `> ` block (`> ` with up to three spaces before it, one space after it optional), then further blocks only while each starts with a comment head. A first block that is not a comment stays a plain quote; what follows the blocks is the rest. `read_review`, `read_plain_quote_first`, `read_second_plain_block_stays_text`, `read_no_quote`
- Every kind reads back to what was written. The picked words end at the first line without a `>`, so a note may itself start with `>`. `read_each_kind`, `read_note_starting_with_quote_mark`

## Cases

`cases.json` holds `{"version": 1, "cases": [...]}`. Each case has `name`, `description`, `source`, `input` and `expected`.

- **Writing** (`write_*`): `input` is `{"comments": [<comment>], "text": "<typed>"}`; `expected` is `{"message": "<the message as sent>"}`.
- **Reading** (`read_*`): `input` is `{"message": "<a sent message>"}`; `expected` is `null` when the message does not start with a `> ` block, else `{"quote": "<plain first block>" | null, "comments": [<comment>], "rest": "<what follows>"}`.

A `<comment>` is `{"quoted": "<the words, the code or the HTML>", "note": "<the note>"}` with at most one of:

- `"code"`: `{"file", "from", "to", "old"?, "diff"?, "lang"?, "omitted"?}`. `diff` is the `Diff:` value as written; a missing `old` is `false` and a missing `omitted` is `0`; a missing `diff` or `lang` is none.
- `"element"`: `{"url", "selector", "component"?, "source"?, "text"?, "omitted"?}`. A missing `omitted` is `0`; a missing `component`, `source` or `text` is none.

A comment with neither is a comment on picked words. Comments compare by these fields only; the order of comments matters.

## Checking

- Web: `cd web && npx vitest run src/lib/comments.spec-fixtures.test.ts`.
- iOS: `PersonaTests/MessageCommentsTests` (`readsAndWritesTheSharedCases`).

Add a case by writing its expected text by hand from the protocol and the web's goldens, then run both.
