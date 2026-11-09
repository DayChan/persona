package node

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// ignoreTimeout bounds the one git process a listing starts to learn which names Git ignores.
const ignoreTimeout = 3 * time.Second

// markIgnored sets Ignored on the entries of dir (a real path) that Git ignores, with one `git check-ignore` for the
// whole listing. Outside a repository, without git, or when it takes too long nothing is marked: the tree is then
// the same as before, never wrong. Tracked files are never reported ignored (check-ignore skips the index).
func markIgnored(dir string, list []DirEntry) {
	if len(list) == 0 {
		return
	}
	var in bytes.Buffer
	for _, e := range list {
		// check-ignore reads every line as a pathspec: a name such as ":!x" or ":(glob)y" would be magic it refuses,
		// failing the whole listing. A "./" prefix makes every name a plain path (git accepts "/" on Windows too).
		in.WriteString("./")
		in.WriteString(e.Name)
		in.WriteByte(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), ignoreTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "-c", "core.quotepath=off", "--no-pager", "check-ignore", "-z", "--stdin")
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = &in
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	// Exit 1: nothing ignored; 128: not a repository. Both leave the list as it is.
	if err := cmd.Run(); err != nil && out.Len() == 0 {
		return
	}
	ignored := map[string]bool{}
	for _, name := range strings.Split(out.String(), "\x00") {
		if name = strings.TrimPrefix(name, "./"); name != "" {
			ignored[name] = true
		}
	}
	for i := range list {
		list[i].Ignored = ignored[list[i].Name]
	}
}
