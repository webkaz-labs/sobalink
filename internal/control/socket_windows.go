package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/Microsoft/go-winio"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"golang.org/x/sys/windows"
	"net"
	"path/filepath"
	"strings"
)

func endpoint(dir string) string {
	p, _ := filepath.Abs(dir)
	s := sha256.Sum256([]byte(strings.ToLower(p)))
	return `\\.\pipe\tsnet-bridge-` + hex.EncodeToString(s[:16])
}
func listen(dir string) (net.Listener, error) {
	s, e := config.SecurityDescriptor(false)
	if e != nil {
		return nil, e
	}
	return winio.ListenPipe(endpoint(dir), &winio.PipeConfig{SecurityDescriptor: s, InputBufferSize: 4096, OutputBufferSize: 65536})
}
func dial(ctx context.Context, dir string) (net.Conn, error) {
	c, e := winio.DialPipeContext(ctx, endpoint(dir))
	if e != nil {
		return nil, e
	}
	if e = verifyPipeServer(c); e != nil {
		c.Close()
		return nil, e
	}
	return c, nil
}

func Unavailable(e error) bool {
	return errors.Is(e, windows.ERROR_FILE_NOT_FOUND) || errors.Is(e, windows.ERROR_PATH_NOT_FOUND)
}

// A server DACL alone does not prevent another account squatting a predictable
// pipe name. Verify the actual server token before sending any command.
func verifyPipeServer(c net.Conn) error {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("named pipe handle unavailable")
	}
	var pid uint32
	if e := windows.GetNamedPipeServerProcessId(windows.Handle(f.Fd()), &pid); e != nil {
		return e
	}
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if e != nil {
		return e
	}
	defer windows.CloseHandle(h)
	var token windows.Token
	if e = windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token); e != nil {
		return e
	}
	defer token.Close()
	u, e := token.GetTokenUser()
	if e != nil {
		return e
	}
	sid, e := config.UserSID()
	if e != nil {
		return e
	}
	if u.User.Sid.String() != sid {
		return errors.New("named pipe server belongs to another user")
	}
	return nil
}
