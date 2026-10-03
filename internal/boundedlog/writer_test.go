package boundedlog

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func testWriter(t *testing.T, limit int64) (*Writer, *os.File) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(t.TempDir(), "output.log"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	writer, err := New(file, limit)
	if err != nil {
		t.Fatal(err)
	}
	return writer, file
}

func TestWriterBoundsContinuousAndOversizedWrites(t *testing.T) {
	const limit = 1024
	writer, file := testWriter(t, limit)
	var history []byte
	for i := range 1000 {
		data := []byte(fmt.Sprintf("record %04d: recent application output\n", i))
		history = append(history, data...)
		if n, err := writer.Write(data); err != nil || n != len(data) {
			t.Fatal(n, err)
		}
		got, err := os.ReadFile(file.Name())
		if err != nil || len(got) > limit || !bytes.HasSuffix(history, got) || !bytes.HasSuffix(got, data) {
			t.Fatalf("write %d: retained %d bytes: %v", i, len(got), err)
		}
		if len(history) >= limit && len(got) < limit/2 {
			t.Fatalf("write %d retained too little recent output: %d", i, len(got))
		}
	}
	large := append(bytes.Repeat([]byte("old data\n"), 1<<16), []byte("latest output\n")...)
	if n, err := writer.Write(large); err != nil || n != len(large) {
		t.Fatal(n, err)
	}
	got, err := os.ReadFile(file.Name())
	if err != nil || !bytes.Equal(got, large[len(large)-limit:]) {
		t.Fatal("large write did not retain the exact finite tail", len(got), err)
	}
}

func TestWriterConcurrentOutput(t *testing.T) {
	const limit = 4096
	writer, file := testWriter(t, limit)
	var writers sync.WaitGroup
	for i := range 8 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for j := range 200 {
				if _, err := fmt.Fprintf(writer, "writer %d output %d\n", i, j); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	writers.Wait()
	if _, err := writer.Write([]byte("last\n")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file.Name())
	if err != nil || len(got) > limit || !bytes.HasSuffix(got, []byte("last\n")) {
		t.Fatal(len(got), err)
	}
}

func TestWriterTrimsExistingOversizedFile(t *testing.T) {
	_, file := testWriter(t, 16)
	data := []byte("old output and the recent tail")
	if _, err := file.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := New(file, 16); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file.Name())
	if err != nil || !bytes.Equal(got, data[len(data)-8:]) {
		t.Fatal(string(got), err)
	}
}

func TestWriterReportsRolloverAndClosedFileFailures(t *testing.T) {
	writer, file := testWriter(t, 16)
	initial := []byte("0123456789abcdef")
	if _, err := writer.Write(initial); err != nil {
		t.Fatal(err)
	}
	readOnly, err := os.Open(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	writer, err = New(readOnly, 16)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := writer.Write([]byte("latest")); n != 0 || err == nil {
		t.Fatal("read-only rollover accepted", n, err)
	}
	got, err := os.ReadFile(file.Name())
	if err != nil || !bytes.Equal(got, initial) {
		t.Fatal("failed rollover changed existing output", string(got), err)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := writer.Write([]byte("latest")); n != 0 || err == nil {
		t.Fatal("closed writer accepted output", n, err)
	}
}

func TestWriterRejectsInvalidLimitAndNonregularFile(t *testing.T) {
	_, file := testWriter(t, 16)
	if _, err := New(file, 0); err == nil {
		t.Fatal("zero limit accepted")
	}
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if _, err := New(dir, 16); err == nil {
		t.Fatal("directory accepted")
	}
}
