package distribution

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func within(root, path string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

func sourceEpoch() (time.Time, error) {
	value := os.Getenv("SOURCE_DATE_EPOCH")
	if value == "" {
		value = "0"
	}
	epoch, err := strconv.ParseInt(value, 10, 64)
	if err != nil || epoch < 0 || epoch > 0xffffffff {
		return time.Time{}, errors.New("SOURCE_DATE_EPOCH must be an integer between 0 and 4294967295")
	}
	return time.Unix(epoch, 0).UTC(), nil
}

// Archive normalizes entry order, permissions, owners and timestamps. Symlinks
// and special files are rejected, including a symlink as the source root.
func Archive(source, destination string) (err error) {
	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	if within(source, destination) {
		return errors.New("archive destination must be outside its source")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("archive source must be a real directory")
	}
	epoch, err := sourceEpoch()
	if err != nil {
		return err
	}
	isZIP := strings.HasSuffix(destination, ".zip")
	if !isZIP && !strings.HasSuffix(destination, ".tar.gz") {
		return errors.New("archive must end with .zip or .tar.gz")
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(destination), ".archive-*")
	if err != nil {
		return err
	}
	temporary := out.Name()
	defer func() { _ = os.Remove(temporary) }()
	var zw *zip.Writer
	var tw *tar.Writer
	var gz *gzip.Writer
	if isZIP {
		zw = zip.NewWriter(out)
	} else {
		gz = gzip.NewWriter(out)
		gz.Header.ModTime = epoch
		gz.Header.OS = 255
		tw = tar.NewWriter(gz)
	}
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular archive entry: %s", entry.Name())
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		mode := int64(0o644)
		// Explicit package layout makes Windows/Unix archive modes identical.
		if info.IsDir() || strings.HasPrefix(name, "bin/") {
			mode = 0o755
		}
		if info.IsDir() {
			name += "/"
		}
		var writer io.Writer
		if isZIP {
			stamp := epoch
			if stamp.Year() < 1980 {
				stamp = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: stamp}
			fileMode := os.FileMode(mode)
			if info.IsDir() {
				fileMode |= os.ModeDir
				header.Method = zip.Store
			}
			header.SetMode(fileMode)
			writer, err = zw.CreateHeader(header)
		} else {
			kind := byte(tar.TypeReg)
			size := info.Size()
			if info.IsDir() {
				kind = tar.TypeDir
				size = 0
			}
			err = tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: size, Typeflag: kind, ModTime: epoch, Format: tar.FormatPAX})
			writer = tw
		}
		if err != nil || info.IsDir() {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, file)
		return errors.Join(copyErr, file.Close())
	})
	if isZIP {
		err = errors.Join(err, zw.Close())
	} else {
		err = errors.Join(err, tw.Close(), gz.Close())
	}
	err = errors.Join(err, out.Chmod(0o644), out.Close())
	if err != nil {
		return err
	}
	// Remove only an existing regular generated archive, never a directory/link.
	if info, statErr := os.Lstat(destination); statErr == nil {
		if !info.Mode().IsRegular() {
			return errors.New("archive destination is not a regular file")
		}
		if err = os.Remove(destination); err != nil {
			return err
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	return os.Rename(temporary, destination)
}
