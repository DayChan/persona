package node

// readPaneEnv reads the engine pane variables from the process's environment block (its PEB, procs_windows.go).
func readPaneEnv(pid int) map[string]string {
	if pid <= 0 {
		return nil
	}
	p, err := readWinProc(uint32(pid), paneEnvKeys)
	if err != nil {
		return nil
	}
	return p.env
}
