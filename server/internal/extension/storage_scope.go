package extension

import (
	"context"
	"errors"
	"time"
)

// StorageScope is trusted manager metadata, never source-supplied request params.
// Data identity is the persisted installation epoch, not the public name alone.
type StorageScope struct {
	Plugin      string
	PublicID    string
	InstalledAt time.Time
	UpdatedAt   time.Time
	Run         string
	Granted     *StoragePermission
	// Owner is set only from authenticated manager caller metadata for explicit
	// restore, never from a source operation or HTTP request body.
	Owner bool
	// Start is set only by the exact host initialization attempt to bind a new
	// runtime. Ordinary calls cannot replace the active run with a stale one.
	Start bool
}

// StorageRetentionStore detaches business state and deletes installation secrets
// atomically on uninstall; legacy Stores keep their existing contract.
type StorageRetentionStore interface {
	DeleteExtensionRecordRetainingData(context.Context, Record, bool) error
}

// Storage scope errors carry categories only, never data, logical key content,
// SQL details, account credentials or a host-generated opaque handle.
var (
	ErrStorageScope  = errors.New("storage installation or runtime retired")
	ErrStorageQuota  = errors.New("storage quota exceeded")
	ErrStorageDenied = errors.New("storage permission denied")
)
