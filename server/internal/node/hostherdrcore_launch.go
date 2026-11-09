package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/herdrcore"
	"github.com/nwverse/persona/server/internal/host"
)

// Starting sessions in the Herdr app (#2532, docs/plans/plan-orca-support.md "herdr-core host"). A new terminal opens the
// way the app opens one — the machine's shell, kept, with the app's terminal identity and labels — and goes into the
// app's layout document as a tab of its own, so the app shows it like a tab the user opened. The document is the app's:
// the node edits it as generic JSON, only adding a space or a tab and renaming, so whatever else the app keeps there is
// written back as it was, and writes it only at the version it read (doc.set), redoing the change after a conflict. A
// document the node does not recognize, or none (the app never ran on this core), is never written: the terminal then
// starts unplaced, grouped by its directory like one `hc run` started.

// herdrSeenLabel is the turn the app last showed of a terminal; a new one has shown none.
const herdrSeenLabel = "herdr.seen"

// hcAppTerminalEnv is the terminal identity the Herdr app gives the shells it opens (its viewer is Ghostty's).
var hcAppTerminalEnv = map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "TERM_PROGRAM": "ghostty"}

// layoutTries bounds one change of the layout document: each conflict (the app wrote it meanwhile) reads it again.
const layoutTries = 4

// errUnknownLayout: there is no layout document, or not one in the form the node edits.
var errUnknownLayout = fmt.Errorf("%w: Herdr App 的布局不是 Persona 认识的格式，没有改动", host.ErrUnsupported)

// errHerdrCoreDirSpace: a terminal the app does not place (started with hc run) is grouped by its directory, which has
// no name to change.
var errHerdrCoreDirSpace = fmt.Errorf("%w: 这些终端不在 Herdr App 的空间里（按目录归组），不能改名", host.ErrUnsupported)

// appLayout is the app's layout document as generic JSON: spaces, each with a folder, a name and tabs; a tab has a name
// and a root of rows and columns whose blocks name terminals.
type appLayout struct {
	doc    map[string]any
	spaces []any
}

// parseAppLayout decodes the document, numbers kept as written (a split's weights), and checks the parts the node edits:
// errUnknownLayout when it is missing or anything there is not as expected.
func parseAppLayout(raw json.RawMessage) (*appLayout, error) {
	if raw == nil {
		return nil, errUnknownLayout
	}
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&doc) != nil {
		return nil, errUnknownLayout
	}
	spaces, ok := doc["spaces"].([]any)
	if !ok {
		return nil, errUnknownLayout
	}
	for _, s := range spaces {
		sm, ok := s.(map[string]any)
		if !ok || !isLayoutID(sm["id"]) || !isOptString(sm["name"]) || !isOptString(sm["folder"]) {
			return nil, errUnknownLayout
		}
		tabs, ok := sm["tabs"].([]any)
		if !ok {
			return nil, errUnknownLayout
		}
		for _, t := range tabs {
			tm, ok := t.(map[string]any)
			if !ok || !isLayoutID(tm["id"]) || !isOptString(tm["name"]) {
				return nil, errUnknownLayout
			}
			if _, ok := tm["root"].(map[string]any); !ok {
				return nil, errUnknownLayout
			}
		}
	}
	return &appLayout{doc: doc, spaces: spaces}, nil
}

func isLayoutID(v any) bool {
	s, ok := v.(string)
	return ok && s != ""
}

func isOptString(v any) bool {
	_, ok := v.(string)
	return v == nil || ok
}

func layoutString(v any) string {
	s, _ := v.(string)
	return s
}

// value is the document to write back.
func (l *appLayout) value() map[string]any {
	l.doc["spaces"] = l.spaces
	return l.doc
}

func (l *appLayout) space(id string) map[string]any {
	for _, s := range l.spaces {
		if sm := s.(map[string]any); sm["id"] == id {
			return sm
		}
	}
	return nil
}

// folderSpace finds the app space for this directory regardless of its displayed name.
func (l *appLayout) folderSpace(cwd string) map[string]any {
	for _, s := range l.spaces {
		sm := s.(map[string]any)
		if folder := layoutString(sm["folder"]); folder != "" && filepath.Clean(folder) == filepath.Clean(cwd) {
			return sm
		}
	}
	return nil
}

func (l *appLayout) tab(id string) map[string]any {
	for _, s := range l.spaces {
		for _, t := range s.(map[string]any)["tabs"].([]any) {
			if tm := t.(map[string]any); tm["id"] == id {
				return tm
			}
		}
	}
	return nil
}

// addSpace appends a space for folder, as the app makes one for a folder: no name of its own unless given one.
func (l *appLayout) addSpace(id, folder, name string) map[string]any {
	s := map[string]any{"id": id, "name": name, "folder": folder, "tabs": []any{}}
	l.spaces = append(l.spaces, s)
	return s
}

// addTab appends a tab holding one block with the terminal, shaped as the app's own (the root row shares the tab's id).
func addTab(space map[string]any, id, block, name, terminal string) {
	tab := map[string]any{"id": id, "name": name, "root": map[string]any{"id": id, "type": "row", "children": []any{
		map[string]any{"id": block, "type": "block", "content": map[string]any{"terminal": terminal}},
	}}}
	space["tabs"] = append(space["tabs"].([]any), tab)
}

// spaceTitle is what the app shows for a space: its name, else its folder's.
func spaceTitle(space map[string]any) string {
	return firstOf(layoutString(space["name"]), filepath.Base(layoutString(space["folder"])))
}

// layoutID is a new id in the app's form: its kind's prefix and 32 hex digits.
func layoutID(prefix string) string { return prefix + strings.ReplaceAll(newUUID(), "-", "") }

// editLayout reads the layout document, applies change and writes it at the version read; after a conflict it does
// it again on the new document, so change must work from what it is given.
func (h *herdrCoreHost) editLayout(ctx context.Context, change func(*appLayout) error) error {
	for try := 1; ; try++ {
		raw, version, err := h.c.DocRaw(ctx, herdrAppDoc)
		if err != nil {
			return hcErr(err)
		}
		l, err := parseAppLayout(raw)
		if err != nil {
			return err
		}
		if err := change(l); err != nil {
			return err
		}
		err = h.c.DocSet(ctx, herdrAppDoc, l.value(), version)
		if !herdrcore.IsKind(err, "conflict") || try == layoutTries {
			return hcErr(err)
		}
	}
}

func (h *herdrCoreHost) CreateSpace(ctx context.Context, cwd, label string) (*host.Created, error) {
	return h.createTerminal(ctx, "", cwd, label, "")
}

func (h *herdrCoreHost) CreateSlot(ctx context.Context, space, cwd, label string) (*host.Created, error) {
	return h.createTerminal(ctx, space, cwd, "", label)
}

// createTerminal opens a shell in cwd as a new tab named tabName of the app's space. An automatic or directory-group
// start reuses the space for cwd, else adds one named spaceName unless that is its folder's name. Nothing is typed. A
// terminal the layout document did not take is ended, not left running where the app does not show it.
func (h *herdrCoreHost) createTerminal(ctx context.Context, space, cwd, spaceName, tabName string) (*host.Created, error) {
	inSpace := space != "" && !strings.HasPrefix(space, dirSpace)
	raw, _, err := h.c.DocRaw(ctx, herdrAppDoc)
	if err != nil {
		return nil, hcErr(err)
	}
	l, err := parseAppLayout(raw)
	if err != nil {
		if inSpace {
			return nil, host.Mark(fmt.Errorf("herdr-core: cannot resolve app space %s: %w", space, err), host.ErrNotDelivered)
		}
		return h.createUnplaced(ctx, cwd)
	}
	if spaceName == filepath.Base(cwd) {
		spaceName = ""
	}
	newSpaceID := layoutID("sp")
	chooseSpace := func(l *appLayout) (map[string]any, error) {
		if inSpace {
			if s := l.space(space); s != nil {
				return s, nil
			}
			return nil, errNoAppSpace(space)
		}
		if s := l.folderSpace(cwd); s != nil {
			return s, nil
		}
		return l.addSpace(newSpaceID, cwd, spaceName), nil
	}
	s, err := chooseSpace(l)
	if err != nil {
		return nil, err
	}
	spaceID := layoutString(s["id"])
	tabID, block := layoutID("tb"), layoutID("p")
	where := func(s map[string]any) string {
		return spaceTitle(s) + " / " + firstOf(tabName, strconv.Itoa(len(s["tabs"].([]any))+1))
	}
	labels := map[string]string{
		herdrSpaceLabel: spaceID, herdrTabLabel: tabID, herdrSeenLabel: "0",
		herdrWhereLabel: where(s),
	}
	t, err := h.openShell(ctx, cwd, labels)
	if err != nil {
		return nil, hcErr(err)
	}
	err = h.editLayout(ctx, func(l *appLayout) error {
		s, err := chooseSpace(l)
		if err != nil {
			return err
		}
		spaceID = layoutString(s["id"])
		if labels[herdrSpaceLabel] != spaceID || labels[herdrWhereLabel] != where(s) {
			labels[herdrSpaceLabel], labels[herdrWhereLabel] = spaceID, where(s)
			if err := h.c.Label(ctx, t.Address, map[string]string{
				herdrSpaceLabel: spaceID, herdrWhereLabel: labels[herdrWhereLabel],
			}); err != nil {
				return hcErr(err)
			}
		}
		addTab(s, tabID, block, tabName, t.Address)
		return nil
	})
	if err != nil {
		return nil, h.failedTerminal(ctx, t, fmt.Errorf("herdr-core: add the new terminal to the Herdr app: %w", err))
	}
	return &host.Created{SpaceID: spaceID, SlotID: tabID, Pane: host.PaneRef{Kind: host.HerdrCore, ID: t.Address}, Shell: t.Argv[0]}, nil
}

// createUnplaced opens the shell outside the app's layout, in the directory group of an `hc run` terminal.
func (h *herdrCoreHost) createUnplaced(ctx context.Context, cwd string) (*host.Created, error) {
	t, err := h.openShell(ctx, cwd, nil)
	if err != nil {
		return nil, hcErr(err)
	}
	return &host.Created{SpaceID: dirSpace + t.Cwd, Pane: host.PaneRef{Kind: host.HerdrCore, ID: t.Address}, Shell: t.Argv[0]}, nil
}

// openShell records the core's actual shell, which can differ from the node's login shell. An unknown program
// cannot safely select command syntax, even if a later terminal.get happens to show a shell in front.
func (h *herdrCoreHost) openShell(ctx context.Context, cwd string, labels map[string]string) (*herdrcore.Terminal, error) {
	t, err := h.c.Run(ctx, herdrcore.RunOpts{Cwd: cwd, Env: hcAppTerminalEnv, Keep: true, NoParent: true, Labels: labels})
	if err != nil {
		return nil, hcErr(err)
	}
	if len(t.Argv) == 0 || !shellProgram(t.Argv[0]) {
		err := host.Mark(fmt.Errorf("herdr-core: %s did not report its shell", t.Address), host.ErrNotDelivered)
		return nil, h.failedTerminal(ctx, t, err)
	}
	return t, nil
}

// failedTerminal closes an owned terminal after a failed start and preserves both the start and cleanup errors.
func (h *herdrCoreHost) failedTerminal(ctx context.Context, t *herdrcore.Terminal, err error) error {
	if closeErr := h.Close(context.WithoutCancel(ctx), host.PaneRef{Kind: host.HerdrCore, ID: t.Address}, "", ""); closeErr != nil {
		return errors.Join(err, fmt.Errorf("herdr-core: cleanup of %s failed: %w", t.Address, closeErr))
	}
	return err
}

// errNoAppSpace: the space a start was to open in is not in the app any more; nothing was started in it.
func errNoAppSpace(space string) error {
	return host.Mark(fmt.Errorf("herdr-core: the Herdr app has no space %s", space), host.ErrNotDelivered)
}

// Run submits command to the shell in the pane (terminal.send: a paste, then enter). It is refused, with nothing
// typed, unless the shell is what runs there: anything else in front would take the command as its input.
func (h *herdrCoreHost) Run(ctx context.Context, pane host.PaneRef, command string) error {
	t, err := h.c.Get(ctx, pane.ID)
	if err != nil {
		return inputErr(err)
	}
	if t.State != "live" || t.Foreground == nil || !isShell(t.Foreground) {
		return host.Mark(fmt.Errorf("herdr-core: %s does not show its shell", pane.ID), host.ErrNotDelivered)
	}
	return inputErr(h.c.Send(ctx, pane.ID, command))
}

// NameAgent: herdr-core names a terminal only as it starts (terminal.run's name); the node keeps its own name for the
// agent.
func (h *herdrCoreHost) NameAgent(context.Context, host.PaneRef, string) error { return nil }

// RenameSpace names the app's space in its layout document.
func (h *herdrCoreHost) RenameSpace(ctx context.Context, space, name string) error {
	if strings.HasPrefix(space, dirSpace) {
		return errHerdrCoreDirSpace
	}
	return h.editLayout(ctx, func(l *appLayout) error {
		s := l.space(space)
		if s == nil {
			return fmt.Errorf("herdr-core: the Herdr app has no space %s", space)
		}
		s["name"] = name
		return nil
	})
}

// RenameSlot names the app's tab in its layout document.
func (h *herdrCoreHost) RenameSlot(ctx context.Context, slot, name string) error {
	return h.editLayout(ctx, func(l *appLayout) error {
		t := l.tab(slot)
		if t == nil {
			return fmt.Errorf("herdr-core: the Herdr app has no tab %s", slot)
		}
		t["name"] = name
		return nil
	})
}
