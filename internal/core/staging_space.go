package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"

	"github.com/webkaz-labs/sobalink/internal/diskspace"
)

func (c *Core) stagingSpace() *diskspace.Guard {
	if c.diskSpace != nil {
		return c.diskSpace
	}
	return diskspace.Process
}

func (c *Core) checkStagingSpace(ctx context.Context) error {
	return c.checkStagingDirectory(ctx, c.dir)
}

func (c *Core) checkStagingDirectory(ctx context.Context, directory string) error {
	volume, err := os.Open(directory)
	if err != nil {
		return diskspace.ErrUnknown
	}
	defer volume.Close()
	return c.stagingSpace().Check(ctx, volume, c.limit("resources", "diskReserveBytes"))
}

type stagingDiskWriter struct {
	core *Core
	ctx  context.Context
	file *os.File
}

func (w stagingDiskWriter) Write(p []byte) (int, error) {
	return w.core.stagingSpace().Write(w.ctx, w.file, p, w.core.limit("resources", "diskReserveBytes"))
}

func replyDiskSpace(w http.ResponseWriter, err error) {
	reply(w, http.StatusInsufficientStorage, map[string]string{"code": networkErrorCode(err), "error": err.Error()})
}

func peerDiskSpaceError(body io.Reader) error {
	var failure struct {
		Code string `json:"code"`
	}
	// Only allowlisted codes are reflected; remote text and paths stay private.
	if json.NewDecoder(io.LimitReader(body, 1024)).Decode(&failure) == nil {
		switch failure.Code {
		case "disk_space_low":
			return &localCommandError{"peer_disk_space_low", "the receiving device reports full storage, a full disk quota, or too little free space; free space, check its quota or review diskReserveBytes there, accept the pending batch if needed, then retry; saved files are preserved"}
		case "disk_space_unknown":
			return &localCommandError{"peer_disk_space_unknown", "the receiving device could not check its available disk space; check its destination volume and permissions, accept the pending batch if needed, then retry; saved files are preserved"}
		}
	}
	return &localCommandError{"peer_storage_unavailable", "the receiving device could not store this transfer; check its storage and receive settings, then retry"}
}
