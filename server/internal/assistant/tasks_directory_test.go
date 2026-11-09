package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func directoryStats(path, kind string) any {
	return map[string]any{"stats": []map[string]string{{"path": path, "kind": kind}}}
}

func statRequestPath(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var in struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || len(in.Paths) != 1 {
		t.Fatalf("directory check request = %s, error = %v", raw, err)
	}
	return in.Paths[0]
}

func TestFlexibleTaskSkipsMachinesWithoutItsDirectory(t *testing.T) {
	for _, cwd := range []string{"/home/me/code/app", "/srv/app"} {
		t.Run(cwd, func(t *testing.T) {
			s, f := launcherHealthHubOn(t, newStorelessService)
			ctx := context.Background()
			blockWorkLauncher(s, "l4")
			// A passing health record must not outrank a candidate that actually has the directory.
			s.recordHealth(ctx, "server", "work", healthResult{ok: true, source: healthSourceCheck})
			var checked []string
			f.nodes = map[string]nodeAnswer{"fs.stat": func(machine string, raw json.RawMessage) (any, error) {
				path := statRequestPath(t, raw)
				checked = append(checked, machine+":"+path)
				kind := "missing"
				if machine == "box" {
					kind = "dir"
				}
				return directoryStats(path, kind), nil
			}}
			task := PlanTask{Machine: "l4", Cwd: cwd, Launcher: "work", MachinePolicy: machineFlexible}
			if !s.healthyPlace(ctx, &task) {
				t.Fatal("did not consider a later candidate with the directory")
			}
			want := cwd
			if cwd == "/home/me/code/app" {
				want = "/Users/me/code/app"
			}
			if task.Machine != "box" || task.Cwd != want || task.Reroute == nil || task.Reroute.ToMachine != "box" {
				t.Fatalf("task moved incorrectly: %+v", task)
			}
			if !reflect.DeepEqual(checked, []string{"server:" + cwd, "box:" + want}) {
				t.Fatalf("checked paths = %v", checked)
			}
		})
	}
}

func TestFlexibleTaskKeepsItsMachineWhenDirectoryCannotBeConfirmed(t *testing.T) {
	for _, result := range []string{"missing", "file", "denied", "rpc error", "empty", "different path"} {
		t.Run(result, func(t *testing.T) {
			s, f := launcherHealthHubOn(t, newStorelessService)
			blockWorkLauncher(s, "l4")
			var checked []string
			f.nodes = map[string]nodeAnswer{"fs.stat": func(machine string, raw json.RawMessage) (any, error) {
				path := statRequestPath(t, raw)
				checked = append(checked, machine)
				switch result {
				case "rpc error":
					return nil, errors.New("unknown method fs.stat")
				case "empty":
					return map[string]any{}, nil
				case "different path":
					return directoryStats("/another/project", "dir"), nil
				default:
					return directoryStats(path, result), nil
				}
			}}
			task := PlanTask{Machine: "l4", Cwd: "/home/me/code/app", Launcher: "work", MachinePolicy: machineFlexible}
			if s.healthyPlace(context.Background(), &task) {
				t.Fatal("moved without confirming the directory")
			}
			if task.Machine != "l4" || task.Cwd != "/home/me/code/app" || task.Reroute != nil {
				t.Fatalf("task location changed: %+v", task)
			}
			if !reflect.DeepEqual(checked, []string{"server", "box"}) {
				t.Fatalf("did not check every candidate: %v", checked)
			}
		})
	}
}

func TestFlexibleTaskDirectoryCheckStillAppliesWhenHealthRecheckCannotPass(t *testing.T) {
	for _, probe := range []string{"throttled", "failed", "unreachable"} {
		t.Run(probe, func(t *testing.T) {
			s, f := launcherHealthHubOn(t, newStorelessService)
			ctx := context.Background()
			failWorkLauncherCheck(s, "l4")
			if probe == "throttled" {
				s.health.mu.Lock()
				s.health.recheckAt[healthKey("l4", "work")] = time.Now()
				s.health.mu.Unlock()
			}
			f.nodes = map[string]nodeAnswer{"fs.stat": func(_ string, raw json.RawMessage) (any, error) {
				return directoryStats(statRequestPath(t, raw), "missing"), nil
			}}
			if probe == "unreachable" {
				f.nodes["launchers.check"] = func(string, json.RawMessage) (any, error) { return nil, rpc.ErrClosed }
			}
			task := PlanTask{Machine: "l4", Cwd: "/home/me/code/app", Launcher: "work", MachinePolicy: machineFlexible}
			if s.healthyPlace(ctx, &task) || task.Machine != "l4" || task.Cwd != "/home/me/code/app" {
				t.Fatalf("health recheck bypassed the directory check: %+v", task)
			}
		})
	}
}

func TestFlexibleTaskNoDirectoryKeepsOriginalStartFailure(t *testing.T) {
	s, f := launcherHealthHubOn(t, newStorelessService)
	ctx := context.Background()
	f.failStart = &rpc.Error{Code: proto.CodeLauncherMissing, Message: "launcher missing in the terminal",
		Data: json.RawMessage(`{"where":"terminal","launcher":"work"}`)}
	f.nodes = map[string]nodeAnswer{"fs.stat": func(_ string, raw json.RawMessage) (any, error) {
		return directoryStats(statRequestPath(t, raw), "missing"), nil
	}}
	todo, err := s.createTodo(ctx, "Inspect project", "Inspect project", "conv9", "plan_directory")
	if err != nil {
		t.Fatal(err)
	}
	task := PlanTask{Title: todo.Title, Prompt: "Inspect project", TodoID: todo.ID,
		Machine: "l4", Cwd: "/home/me/code/app", Launcher: "work", MachinePolicy: machineFlexible}
	p := &TaskPlan{ID: todo.PlanID, Conversation: "conv9", State: planStarted, Tasks: []PlanTask{task}}
	if err := s.savePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	s.startPlanTask(p.ID, p.Conversation, 0, task, false)
	saved, err := s.loadPlan(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := saved.Tasks[0]
	if got.Machine != "l4" || got.Cwd != task.Cwd || got.Reroute != nil || got.Error != f.failStart.Error() {
		t.Fatalf("failed task lost its location or original error: %+v", got)
	}
	if starts := taskStartMachines(f); !reflect.DeepEqual(starts, []string{"l4"}) {
		t.Fatalf("retried without a directory: %v", starts)
	}
	failed := todoNow(s, todo.ID)
	if failed.DoneAt == nil || failed.StartError == "" || len(failed.Agents) != 0 {
		t.Fatalf("failed task stayed open: %+v", failed)
	}
}
