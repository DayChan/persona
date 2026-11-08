package nodeclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// UpdateInventoryTarget is bound by the local authority reader to the original
// manager settings, then echoed by the live node. It does not select a default.
type UpdateInventoryTarget struct {
	Manager           string `json:"manager"`
	Main              string `json:"main"`
	Machine           string `json:"machine_id"`
	MachineUID        string `json:"machine_uid"`
	AuthorityRevision string `json:"authority_revision"`
}

// UpdateInventory contains no prompts, task commands, paths or transcripts.
// Complete means all configured sources answered; it does not mean idle.
type UpdateInventory struct {
	Schema      int                   `json:"schema"`
	Capability  string                `json:"capability"`
	Complete    bool                  `json:"complete"`
	Target      UpdateInventoryTarget `json:"target"`
	Instance    string                `json:"instance"`
	Version     string                `json:"version"`
	Revision    string                `json:"revision"`
	Sources     map[string]string     `json:"sources"`
	Rows        []UpdateActivity      `json:"rows"`
	CompletedAt time.Time             `json:"completed_at"`
}

type UpdateActivity struct {
	Source              string `json:"source"`
	Session             string `json:"session_id"`
	Live                bool   `json:"live"`
	Status              string `json:"status"`
	Starting            bool   `json:"starting"`
	Background          int    `json:"background"`
	Subagents           int    `json:"subagents"`
	Compacting          bool   `json:"compacting"`
	PendingInteractions int    `json:"pending_interactions"`
	Revision            string `json:"revision"`
}

// InventoryRevision binds every row and source revision to the explicit target.
// Completion timestamps are excluded so two fresh reads can be compared.
func InventoryRevision(in UpdateInventory) (string, error) {
	data, err := json.Marshal(struct {
		Instance string
		Version  string
		Target   UpdateInventoryTarget
		Sources  map[string]string
		Rows     []UpdateActivity
	}{in.Instance, in.Version, in.Target, in.Sources, in.Rows})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
