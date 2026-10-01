//go:build !windows

package control

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

func endpoint(dir string) string { return filepath.Join(dir, "control.sock") }
func listen(dir string) (net.Listener, error) {
	p := endpoint(dir)
	if len(p) > 100 {
		return nil, errors.New("profile path is too long for a Unix socket; use a shorter --state-dir")
	}
	if s, e := os.Lstat(p); e == nil {
		if s.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("control path is not a socket")
		}
		if e = os.Remove(p); e != nil {
			return nil, e
		}
	}
	ln, e := net.Listen("unix", p)
	if e != nil {
		return nil, e
	}
	if e = os.Chmod(p, 0600); e != nil {
		ln.Close()
		return nil, e
	}
	return ln, nil
}
func dial(ctx context.Context, dir string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", endpoint(dir))
}

func Unavailable(e error) bool {
	return errors.Is(e, os.ErrNotExist) || errors.Is(e, syscall.ECONNREFUSED)
}
