package storage

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"time"
)

type CreateBlob struct {
	RequestID string `json:"request_id"`
	Key       string `json:"key"`
}

type WriteBlob struct {
	RequestID string `json:"request_id"`
	Key       string `json:"key"`
	Handle    string `json:"handle"`
	Offset    int    `json:"offset"`
	Data      string `json:"data"`
}

type ReadBlob struct {
	Key    string `json:"key"`
	Handle string `json:"handle"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type MutateBlob struct {
	RequestID string `json:"request_id"`
	Key       string `json:"key"`
	Handle    string `json:"handle"`
}

func ownedBlob(state *State, scope Scope, handle string) (*Blob, error) {
	if handle == "" || len(handle) > 80 {
		return nil, ErrInvalid
	}
	blob := state.Blobs[handle]
	if blob == nil {
		return nil, ErrNotFound
	}
	if err := allowed(scope.Granted, blob.Key); err != nil {
		return nil, err
	}
	return blob, nil
}

func blobCall(state *State, scope Scope, method string, params json.RawMessage, now time.Time) (any, bool, error) {
	if !scope.Granted.Blobs {
		return nil, false, ErrDenied
	}
	switch method {
	case "storage.blob.create":
		var in CreateBlob
		if err := decode(params, &in); err != nil {
			return nil, false, err
		}
		if err := allowed(scope.Granted, in.Key); err != nil {
			return nil, false, err
		}
		return mutation(state, method, in.RequestID, in, now, func(copy *State) (any, error) {
			handle := rand.Text()
			copy.Blobs[handle] = &Blob{Key: in.Key, Chunks: []string{}}
			return map[string]string{"handle": handle}, nil
		})
	case "storage.blob.write":
		var in WriteBlob
		if err := decode(params, &in); err != nil || in.Offset < 0 {
			return nil, false, ErrInvalid
		}
		if err := allowed(scope.Granted, in.Key); err != nil {
			return nil, false, err
		}
		data, err := decodeData(in.Data, ChunkMost)
		if err != nil {
			return nil, false, err
		}
		if len(data) == 0 {
			return nil, false, ErrInvalid
		}
		in.Data = base64.StdEncoding.EncodeToString(data)
		return mutation(state, method, in.RequestID, in, now, func(copy *State) (any, error) {
			blob := copy.Blobs[in.Handle]
			if blob == nil {
				return nil, ErrNotFound
			}
			if blob.Key != in.Key {
				return nil, ErrDenied
			}
			if blob.Complete || in.Offset != blob.Bytes {
				return nil, ErrConflict
			}
			if blob.Bytes > DataMost-len(data) || len(blob.Chunks) >= DataMost/ChunkMost+1 {
				return nil, ErrQuota
			}
			blob.Chunks = append(blob.Chunks, in.Data)
			blob.Bytes += len(data)
			return map[string]int{"bytes": blob.Bytes}, nil
		})
	case "storage.blob.finish", "storage.blob.delete":
		var in MutateBlob
		if err := decode(params, &in); err != nil {
			return nil, false, err
		}
		if err := allowed(scope.Granted, in.Key); err != nil {
			return nil, false, err
		}
		return mutation(state, method, in.RequestID, in, now, func(copy *State) (any, error) {
			blob := copy.Blobs[in.Handle]
			if blob == nil {
				return nil, ErrNotFound
			}
			if blob.Key != in.Key {
				return nil, ErrDenied
			}
			if method == "storage.blob.delete" {
				delete(copy.Blobs, in.Handle)
				return map[string]bool{"deleted": true}, nil
			}
			blob.Complete = true
			return map[string]any{"handle": in.Handle, "bytes": blob.Bytes, "complete": true}, nil
		})
	case "storage.blob.read":
		var in ReadBlob
		if err := decode(params, &in); err != nil || in.Offset < 0 || in.Limit <= 0 || in.Limit > ChunkMost {
			return nil, false, ErrInvalid
		}
		if err := allowed(scope.Granted, in.Key); err != nil {
			return nil, false, err
		}
		blob, err := ownedBlob(state, scope, in.Handle)
		if err != nil {
			return nil, false, err
		}
		if blob.Key != in.Key {
			return nil, false, ErrDenied
		}
		if !blob.Complete || in.Offset > blob.Bytes {
			return nil, false, ErrConflict
		}
		result := make([]byte, 0, min(in.Limit, blob.Bytes-in.Offset))
		position := 0
		for _, chunk := range blob.Chunks {
			data, err := decodeData(chunk, ChunkMost)
			if err != nil {
				return nil, false, ErrCorrupt
			}
			if position+len(data) > in.Offset && len(result) < in.Limit {
				at := max(0, in.Offset-position)
				count := min(len(data)-at, in.Limit-len(result))
				result = append(result, data[at:at+count]...)
			}
			position += len(data)
			if len(result) == in.Limit {
				break
			}
		}
		return map[string]any{"data": base64.StdEncoding.EncodeToString(result), "offset": in.Offset, "bytes": blob.Bytes, "eof": in.Offset+len(result) == blob.Bytes}, false, nil
	default:
		return nil, false, ErrInvalid
	}
}
