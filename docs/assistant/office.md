# Generating Office files (`personad office`, #1300)

The command the assistant and Agents use to generate Word, PowerPoint and Excel files; it is M9 of #1289. The file is generated on that machine. When the reply contains its absolute path, the App shows it as a card under the reply
(a thumbnail of the first page, the file name, a type such as "Word document", #1543, see `docs/apple/ios.md` ("Image and document cards seen in this turn")); tapping it opens the M1–M3 preview.
The assistant and Agents started by Persona use it through the skill `persona-office` (see "skill" below).

## Why it lives in personad

The hub machine that runs the assistant does not necessarily have Node, pip / venv or LibreOffice (observed: Ubuntu lacks `ensurepip`, and Claude Code is a native binary).
Approaches that depend on python-docx / python-pptx (lxml, which needs compiling) or Node's docx / pptxgenjs do not run there, and the environments on the various computers are not complete either.
Every machine has `personad`, so the generator lives in it (`server/internal/office`). Go has no mature docx / pptx library with a suitable license, so we write minimal valid Office Open XML ourselves;
Markdown is parsed with goldmark (MIT). We borrowed ideas from Anthropic's official docx / xlsx / pptx skills (self-check after writing, formulas in tables, consistent layout): only the ideas, no content was copied.

## skill (persona-office)

Built-in skill `server/internal/assistantskills/builtin/persona-office`: SKILL.md says when to use it, how to write the source file, to look at `check` after generating, and to write the file's absolute path in the reply.
The skill's own `office.sh` finds the local personad (first `PERSONA_BIN`, then `~/.persona/bin`, `~/.persona/bin`, `/opt/persona/bin`, finally PATH; if none, exit code 3).

Who gets it (decided by the user 2026-11-04): **only the assistant and Agents started by Persona**. It is not written into the machine's user-level Claude / Codex skills directories, so sessions the user opens themselves are unaffected.

- Assistant: installed into its directory like the other built-in skills (`assistantskills.Sync`: `.claude/skills`, `.agents/skills`; the Codex assistant via extraRoots).
- Claude Agents started by Persona: when node starts, it writes the skills as a plugin (`<node dir>/agent-plugin`, `assistantskills.WritePlugin`; `ForAgents` lists the ones for Agents),
  and the launch command adds `--plugin-dir`. Like `--mcp-config` and Persona's hooks plugin, it applies only to that one session. Inside the session it is called `persona:persona-office`.
  - Added only for Claude Code 2.1 and later (`pluginMinVersion`): older versions reject unknown flags and the whole launch fails. If the version cannot be queried, it is not added either and the launch proceeds as before.
  - The version is queried once on every hello; at launch it is queried again only if more than 10 minutes have passed (each query starts a login shell).
- Codex Agents started by Persona: not included yet. We have not found a way to add a skills directory per session for Codex without writing `CODEX_HOME/skills`.

### No approval every time

The generate command does not pop an approval card on the user's phone: when Persona starts claude, it allows this one command with `--allowedTools`, matching only the absolute path and valid only for that session.
- Agents started by Persona: `Bash(sh <node dir>/agent-plugin/skills/persona-office/scripts/office.sh *)`, added together with the `--plugin-dir` that loads the plugin (Claude Code 2.1 and later).
- Assistant: the same rule for `.claude/skills/persona-office/scripts/office.sh` in its directory, appended after its existing `--allowedTools mcp__persona`.
- Not written into any settings file. Sessions the user opens themselves are unaffected, and the launcher's own `--settings` still applies (since #1462 Persona only passes `--settings` to managed launchers).

Why it works this way (tried on real claude on 2026-11-04):
- Not the skill's `allowed-tools`: with it, invoking the skill itself needs approval (rejected outright under `-p`), and rules hard-coded in SKILL.md cannot carry each machine's paths.
- Not a wildcard like `*/persona-office/scripts/office.sh`: an Agent could write a same-named script in its own directory (no prompt under acceptEdits) and it would be allowed. We tried it and it really ran. Matching only the absolute path blocks this.
- Not written into Persona's settings file: since #1462 most launches no longer pass it.
- Claude Code splits commands chained with `;` or `&&` and checks each one; files under `.claude/` are not modified without asking either. So the assistant's copy of the script cannot be swapped silently.
- `-o` can only write files of the same kind (`.docx` / `.pptx` / `.xlsx`), so one allowed command cannot overwrite files like `~/.zshrc`.
- That is why SKILL.md requires one file per command and the script's absolute path written out, not assembled from shell variables.

## Commands

| Command | What it does |
| --- | --- |
| `personad office docx <file.md> [-o out.docx] [--title T]` | Word document, A4 |
| `personad office pptx <file.md> [-o out.pptx] [--title T]` | 16:9 presentation |
| `personad office xlsx <file.json\|file.csv> [-o out.xlsx]` | Excel workbook |
| `personad office check <file.docx\|.xlsx\|.pptx>` | Reads the structure back, ours or anyone else's |

- Without `-o`, the output goes next to the input with the matching extension. After writing, it prints JSON: `file` (absolute path), `bytes`, `check` (same as the check result).
- Exit codes: 0 success, 1 failure (one line of reason on stderr, e.g. image not found), 2 bad usage.

## What to write

**docx (Markdown)**:
- Supported: `#` to `###` headings (deeper ones count as level three), paragraphs, **bold** / *italic* / `code` / ~~strikethrough~~ / links, ordered and unordered lists (nestable; an ordered list starts from its own first number), GitHub tables (first row is the header, column alignment follows `:---:`), code blocks, quotes, task-list checkboxes, `---` (page break).
- Images `![caption](path relative to the Markdown file)`: PNG / JPEG / GIF only, scaled at most to the page width. Remote images are not downloaded; only the caption text is kept.

**pptx (Markdown)**:
- Slides: if there is `---`, split on it; otherwise each `#` / `##` heading starts a slide. The first heading on a slide is its title.
- Cover: if the first slide has only a `#` heading and paragraphs, it becomes a cover, with the paragraphs as subtitle.
- Body: lists are bullet points (numbered lists keep their numbers); other paragraphs are lines without bullets.
- Images and tables: one image or one table per slide. With text, the image goes on the right and the table below.
- Font size: shrinks automatically with the amount of text on the slide (24 → 20 → 18 → 14 pt). Many viewers do not auto-shrink text the way PowerPoint does while editing.

**xlsx (JSON or CSV)**:

```json
{"sheets":[{"name":"Sales","header":["Month","Revenue","Date"],
  "rows":[["Jan",1200.5,"2026-01-31"],["Total","=SUM(B2:B2)",null]],
  "widths":[10,14,12],"formats":["","#,##0.00","yyyy-mm-dd"],"freeze":true}]}
```

- Cells: text, numbers, `true` / `false`, `null` (empty), or a formula starting with `=`.
- The header is bold, with a light background, and frozen (`freeze: false` turns that off). Column widths default to the content; a CJK character counts as two characters wide.
- `formats` are per-column Excel number formats. When a format looks like a date (contains `yy`, `dd` or `mmm`), text like `"2026-01-31"` becomes a real date.
- The 1900 date system starts at `1900-01-01`; earlier or invalid date strings stay text.
- Sheet names have characters Excel rejects removed, are truncated to 31 characters, and duplicates get a sequence number.
- CSV: the first row is the header. Numeric-looking fields become numbers when the workbook keeps their value: integers such as long IDs must survive exactly, while decimals and exponents may round to the 15 significant digits a spreadsheet keeps (a float written as `0.30000000000000004` becomes `0.3`). IDs with leading zeros such as `007`, non-finite values, integers that would lose digits and decimals that would change further (underflow, subnormals) stay as their original text.

## Formula values

Formulas in the workbook are computed at write time and their values written into the file, like Excel does on save (`formula.go`). Many viewers (QuickLook, the App's preview, many web viewers) do not compute formulas; they only show the stored value, and with no value the cell is blank.
Excel and Numbers still recompute everything on open (`fullCalcOnLoad`).

- Supported: `+ - * / ^ & %`, comparisons, cells and ranges (other sheets too: `Sheet!A1`, `'with space'!A1:B2`), and SUM AVERAGE MIN MAX COUNT COUNTA ROUND ABS IF IFERROR AND OR NOT.
  `ROUND` follows Excel's 15 significant digits: `2.345` rounds to `2.35`.
  Arguments are read as Excel reads them: SUM, AVERAGE, MIN, MAX and COUNT take only the numbers of a reference; typed-in
  arguments also count when they are `TRUE` / `FALSE` or text that reads as a number. AND and OR skip a reference's blanks
  and text (`#VALUE!` when nothing is left to judge).
- When another function appears (VLOOKUP etc.), that formula gets no value and is left for Excel to compute; it will be blank in viewers that do not compute formulas.
- Circular references are written as `#REF!`.

## How it was verified

- Unit tests in `server/internal/office`: every part's XML is valid; content, numbering, images, tables and formula values are all present; check can read them back.
- Synthetic samples cross-checked on macOS (2026-11-04):
  - QuickLook thumbnails: documents, every kind of slide, and workbook formula values all display correctly.
  - `textutil` reads the docx.
  - python-docx / openpyxl / python-pptx open them without errors and read the headings, tables, images, freeze, number formats, dates and cached formula values.
- Worked around several QuickLook limitations:
  - Table borders and widths are written on the table itself, not only in the style.
  - One numbering definition per ordered list, no `startOverride`.
  - Page breaks use "page break before" on the next paragraph, not a break character: QuickLook shows a break character as a box.
- QuickLook still shrinks table column widths to fit the content; in Word they span the full page width.
