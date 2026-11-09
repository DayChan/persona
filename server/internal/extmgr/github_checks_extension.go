package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/github"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A distinct read tool opts an extension into the snapshot contract. The old
// github_checks tool returns human prose and logs and is never interpreted here.
const githubSnapshotTool = "github_ci_snapshot_v1"

var githubSnapshotHead = lazyre.New(`^[0-9a-fA-F]{40,64}$`)

type githubSnapshotV1 struct {
	Version   int    `json:"version"`
	Host      string `json:"host"`
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	Head      string `json:"head"`
	HeadAfter string `json:"head_after"`
	Complete  bool   `json:"complete"`
	CheckRuns []struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"check_runs"`
	Statuses []struct {
		Context string `json:"context"`
		State   string `json:"state"`
	} `json:"statuses"`
}

// githubExtensionChecks calls only the authorized connector's advertised read
// tool and rejects prose, incomplete pages, mismatched targets and changed heads.
func githubExtensionChecks(ctx context.Context, c connector.Connector, repo string, number int) (github.PRCheckSnapshot, time.Time, bool, error) {
	if number <= 0 || !github.ValidRepo(repo) {
		return github.PRCheckSnapshot{}, time.Time{}, false, nil
	}
	if _, ok := connector.As[connector.Accounts](c); !ok {
		return github.PRCheckSnapshot{}, time.Time{}, false, nil
	}
	status := connector.StatusOf(ctx, c)
	if status.State == connector.StateRateLimited {
		return github.PRCheckSnapshot{}, time.Now().Add(time.Hour), true, nil
	}
	if status.State != connector.StateOK {
		return github.PRCheckSnapshot{}, time.Time{}, false, nil
	}
	tools, ok := connector.As[connector.ReadTools](c)
	if !ok {
		return github.PRCheckSnapshot{}, time.Time{}, false, nil
	}
	found := false
	for _, tool := range tools.Tools() {
		if tool.Name == githubSnapshotTool {
			found = true
			break
		}
	}
	if !found {
		return github.PRCheckSnapshot{}, time.Time{}, false, nil
	}
	args, _ := json.Marshal(map[string]any{"repo": repo, "number": number})
	text, err := tools.Call(ctx, githubSnapshotTool, args)
	if err != nil {
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == proto.ToolErrorRateLimited {
			var data struct {
				RetryAfter float64 `json:"retry_after"`
			}
			_ = json.Unmarshal(re.Data, &data)
			delay := time.Hour
			if data.RetryAfter > 0 && data.RetryAfter < 1e9 {
				delay = time.Duration(data.RetryAfter * float64(time.Second))
			}
			return github.PRCheckSnapshot{}, time.Now().Add(delay), true, nil
		}
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "rate limit") || strings.Contains(message, "429") {
			return github.PRCheckSnapshot{}, time.Now().Add(time.Hour), true, nil
		}
		return github.PRCheckSnapshot{}, time.Time{}, true, err
	}
	result, ok := parseGitHubSnapshot(text, repo, number)
	if !ok {
		return github.PRCheckSnapshot{}, time.Time{}, false, nil
	}
	return result, time.Time{}, true, nil
}

func parseGitHubSnapshot(text, repo string, number int) (github.PRCheckSnapshot, bool) {
	var snapshot githubSnapshotV1
	if len(text) > 256*1024 {
		return github.PRCheckSnapshot{}, false
	}
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF || snapshot.Version != 1 ||
		!strings.EqualFold(snapshot.Host, "github.com") || !strings.EqualFold(snapshot.Repo, repo) || snapshot.Number != number ||
		!snapshot.Complete || !githubSnapshotHead.MatchString(snapshot.Head) || snapshot.Head != snapshot.HeadAfter ||
		snapshot.CheckRuns == nil || snapshot.Statuses == nil || len(snapshot.CheckRuns)+len(snapshot.Statuses) > 1000 {
		return github.PRCheckSnapshot{}, false
	}
	result := github.PRCheckSnapshot{Head: snapshot.Head}
	seenRuns, seenStatuses := map[int64]bool{}, map[string]bool{}
	for _, check := range snapshot.CheckRuns {
		if check.ID <= 0 || seenRuns[check.ID] || check.Name == "" || len(check.Name) > 500 {
			return github.PRCheckSnapshot{}, false
		}
		seenRuns[check.ID] = true
		result.AddRun(check.ID, check.Name, check.Status, check.Conclusion)
	}
	for _, status := range snapshot.Statuses {
		if status.Context == "" || len(status.Context) > 500 || seenStatuses[status.Context] {
			return github.PRCheckSnapshot{}, false
		}
		seenStatuses[status.Context] = true
		result.AddStatus(status.Context, status.State)
	}
	result.Finish()
	return result, true
}
