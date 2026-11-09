package node

import (
	"errors"

	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

func navigationError(err error) error {
	if errors.Is(err, transcript.ErrNavigationHistory) {
		return &rpc.Error{Code: "stale_history", Message: "Transcript history changed"}
	}
	if errors.Is(err, transcript.ErrNavigationTarget) {
		return &rpc.Error{Code: "navigation_target_missing", Message: "Navigation target is unavailable"}
	}
	return err
}
