package node

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/nwverse/persona/server/internal/osproc"
)

// Codex on Windows has no ps, lsof or /proc to be found by (codex_discovery.go, codex_openfiles.go): its processes are
// read from the process table and their PEB (procs_windows.go), and what a process holds open from its own handle
// table. Nothing here starts a process; the scan runs every 30 seconds and again before a resume (codexHeldNow).

// scanCodexProcessesOS lists the codex.exe processes — Codex's native binary, which the CLI's npm shim (node) starts
// and the Codex App bundles as its app-server — with their command line and CODEX_HOME / HERDR_PANE_ID. ok is false when
// the process table could not be walked, or a codex.exe of this session could not be read: it may hold a rollout.
func scanCodexProcessesOS() ([]codexProcess, bool) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, false
	}
	defer windows.CloseHandle(snap)
	var out []codexProcess
	ok := true
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if imageName(windows.UTF16ToString(e.ExeFile[:])) != "codex" {
			continue
		}
		p, rerr := readWinProc(e.ProcessID, []string{"CODEX_HOME", herdrPaneEnv})
		if rerr != nil {
			if unreadableHere(int(e.ProcessID)) {
				ok = false
			}
			continue
		}
		argv := p.argv
		if len(argv) == 0 || progName(argv[0]) != "codex" {
			// The image is codex.exe whatever its command line calls it.
			argv = append([]string{"codex.exe"}, argv[min(len(argv), 1):]...)
		}
		if cp, isCodex := codexProcessArgs(int(e.ProcessID), argv, envListing(p.env)); isCodex {
			out = append(out, cp)
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return out, false
	}
	slices.SortFunc(out, func(a, b codexProcess) int { return a.pid - b.pid })
	return out, ok
}

// unreadableHere: a process that could not be read still runs in this logon session (an elevated one of this user's,
// say). One that has exited, or runs in another session (another user's, a service), holds none of this user's
// rollouts.
func unreadableHere(pid int) bool {
	if !osproc.Alive(pid) {
		return false
	}
	var mine, theirs uint32
	if windows.ProcessIdToSessionId(uint32(os.Getpid()), &mine) != nil || windows.ProcessIdToSessionId(uint32(pid), &theirs) != nil {
		return true
	}
	return mine == theirs
}

// scanOpenRolloutsOS lists the rollouts each process has open: the disk files among its handles (duplicated into this
// process for a moment, nothing read from them) whose names are rollouts. ok is false when a process still running
// could not be read. A Codex has a few hundred handles; one process takes a few milliseconds. Asking each rollout who
// holds it (FileProcessIdsUsingFileInformation, the Restart Manager) took 40 ms per file, seconds for a home.
func scanOpenRolloutsOS(processes []codexProcess) (map[int][]string, bool) {
	out := map[int][]string{}
	ok := true
	fileType := fileObjectType()
	for _, p := range processes {
		if p.pid <= 0 {
			continue
		}
		paths, err := openRolloutsOf(uint32(p.pid), fileType)
		if err != nil {
			if unreadableHere(p.pid) {
				ok = false
			}
			continue
		}
		if len(paths) > 0 {
			out[p.pid] = paths
		}
	}
	return out, ok
}

// openRolloutsOf is the rollouts among the files the process pid has open. Only handles of fileType (the kernel's
// File objects; 0 when unknown: all) are looked at, and of those only disk files are asked their name: asking a pipe
// its name can hang behind a read waiting on it, asking its type does not.
func openRolloutsOf(pid uint32, fileType uint32) ([]string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_DUP_HANDLE, false, pid)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	handles, err := processHandles(h)
	if err != nil {
		return nil, err
	}
	self := windows.CurrentProcess()
	var out []string
	for _, e := range handles {
		if fileType != 0 && e.objectType != fileType {
			continue
		}
		var dup windows.Handle
		if windows.DuplicateHandle(h, e.handle, self, &dup, 0, false, windows.DUPLICATE_SAME_ACCESS) != nil {
			continue
		}
		if t, err := windows.GetFileType(dup); err == nil && t == windows.FILE_TYPE_DISK {
			if p := finalPath(dup); rolloutIDRe.MatchString(p) {
				out = append(out, p)
			}
		}
		windows.CloseHandle(dup)
	}
	return out, nil
}

// processHandle is one entry of a process's handle table (PROCESS_HANDLE_TABLE_ENTRY_INFO).
type processHandle struct {
	handle     windows.Handle
	objectType uint32
}

// processHandleEntry lays out PROCESS_HANDLE_TABLE_ENTRY_INFO for its offsets; entries are read as bytes.
type processHandleEntry struct {
	HandleValue      uintptr
	HandleCount      uintptr
	PointerCount     uintptr
	GrantedAccess    uint32
	ObjectTypeIndex  uint32
	HandleAttributes uint32
	Reserved         uint32
}

// maxHandleTable caps the handle table read from one process.
const maxHandleTable = 16 << 20

// processHandles lists the process's handles (ProcessHandleInformation: a count, a reserved word, then the entries).
func processHandles(h windows.Handle) ([]processHandle, error) {
	size := uint32(64 << 10)
	for {
		buf := make([]byte, size)
		var need uint32
		err := windows.NtQueryInformationProcess(h, windows.ProcessHandleInformation, unsafe.Pointer(&buf[0]), size, &need)
		if errors.Is(err, windows.STATUS_INFO_LENGTH_MISMATCH) && size < maxHandleTable {
			size = min(max(need, size*2), maxHandleTable)
			continue
		}
		if err != nil {
			return nil, err
		}
		var e processHandleEntry
		word0 := unsafe.Sizeof(uintptr(0))
		step := unsafe.Sizeof(e)
		n := word(buf, 0)
		var out []processHandle
		for i := uintptr(0); i < n; i++ {
			at := 2*word0 + i*step
			if at+step > uintptr(len(buf)) {
				break
			}
			out = append(out, processHandle{
				handle:     windows.Handle(word(buf, at+unsafe.Offsetof(e.HandleValue))),
				objectType: binary.LittleEndian.Uint32(buf[at+unsafe.Offsetof(e.ObjectTypeIndex):]),
			})
		}
		return out, nil
	}
}

// fileObjectType is the kernel's object type number for File objects, from a handle of this process to NUL; 0 when it
// cannot be told (then every handle is looked at).
var fileObjectType = sync.OnceValue(func() uint32 {
	f, err := os.Open(os.DevNull)
	if err != nil {
		return 0
	}
	defer f.Close()
	handles, err := processHandles(windows.CurrentProcess())
	if err != nil {
		return 0
	}
	for _, e := range handles {
		if uintptr(e.handle) == f.Fd() {
			return e.objectType
		}
	}
	return 0
})

// finalPath is the path of an open file as Windows resolves it, without the \\?\ prefix; "" when it cannot be told.
func finalPath(h windows.Handle) string {
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return ""
		}
		if int(n) < len(buf) {
			p := windows.UTF16ToString(buf[:n])
			if rest, ok := strings.CutPrefix(p, `\\?\UNC\`); ok {
				return `\\` + rest
			}
			return strings.TrimPrefix(p, `\\?\`)
		}
		if n > 1<<15 {
			return ""
		}
		buf = make([]uint16, n+1)
	}
}

// codexStartProgram is the program codex-start runs for a launcher found at path. A batch file would have cmd.exe read
// the message (and the launcher's words) as a command line again: quotes, &, % and line breaks would cut it up or run
// what follows them. npm's codex.cmd only has node start Codex's native codex.exe, so that is run instead, with the
// variables npm's codex.js sets for it; another batch file is run only without a message.
func codexStartProgram(path string, message bool, env []string) (string, []string, error) {
	if ext := strings.ToLower(filepath.Ext(path)); ext != ".cmd" && ext != ".bat" {
		return path, env, nil
	}
	if exe, root := npmCodexNative(path); exe != "" {
		env = withoutEnv(env, []string{"CODEX_MANAGED_*"})
		return exe, append(env, "CODEX_MANAGED_PACKAGE_ROOT="+root, "CODEX_MANAGED_BY_NPM=1"), nil
	}
	if message {
		return "", nil, fmt.Errorf("%s is a batch file: cmd.exe would read the message as commands, so it was not run; put Codex's codex.exe on PATH", path)
	}
	return path, env, nil
}

// npmCodexNative is the codex.exe behind npm's codex.cmd shim (shim is <prefix>\codex.cmd, the package
// <prefix>\node_modules\@openai\codex), found where its bin\codex.js looks: the platform package under the package's
// node_modules, then hoisted next to it, then the package's own vendor directory. root is the package's real path.
func npmCodexNative(shim string) (exe, root string) {
	if !strings.EqualFold(filepath.Base(shim), "codex.cmd") {
		return "", ""
	}
	prefix := filepath.Dir(shim)
	pkg := filepath.Join(prefix, "node_modules", "@openai", "codex")
	if _, err := os.Stat(filepath.Join(pkg, "bin", "codex.js")); err != nil {
		return "", ""
	}
	root = pkg
	if real, err := filepath.EvalSymlinks(pkg); err == nil {
		root = real
	}
	platform, triple := "codex-win32-x64", "x86_64-pc-windows-msvc"
	if runtime.GOARCH == "arm64" {
		platform, triple = "codex-win32-arm64", "aarch64-pc-windows-msvc"
	}
	for _, vendor := range []string{
		filepath.Join(root, "node_modules", "@openai", platform, "vendor"),
		filepath.Join(prefix, "node_modules", "@openai", platform, "vendor"),
		filepath.Join(root, "vendor"),
	} {
		exe := filepath.Join(vendor, triple, "bin", "codex.exe")
		if st, err := os.Stat(exe); err == nil && st.Mode().IsRegular() {
			return exe, root
		}
	}
	return "", ""
}
