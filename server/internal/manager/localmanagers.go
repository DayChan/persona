package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// managersRoot is where the managers of this machine are by default (~/.persona/managers).
func managersRoot() string { return filepath.Dir(assistant.DefaultManagerDir("x")) }

// localManagers are the directories under managersRoot with a manager.json, but the built-in manager's (the
// assistant the hub runs, managerconf.DefaultDir: its manager.json holds only its configuration).
func localManagers() []string {
	entries, _ := os.ReadDir(managersRoot())
	var out []string
	for _, ent := range entries {
		d := filepath.Join(managersRoot(), ent.Name())
		if ent.Name() == assistantwire.AssistantID {
			continue
		}
		if ent.IsDir() && fileExists(filepath.Join(d, assistant.ManagerFile)) {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// onlyManager is the one manager of this machine, for a command without --dir.
func onlyManager() (string, error) {
	list := localManagers()
	switch len(list) {
	case 1:
		return list[0], nil
	case 0:
		return "", fmt.Errorf("%s 下没有管理者：先 personad manager join，或者用 --dir 指定目录", managersRoot())
	}
	return "", fmt.Errorf("这台有 %d 个管理者（%s）：用 --dir 指定是哪个", len(list), strings.Join(list, "、"))
}
