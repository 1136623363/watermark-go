package server

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
)

type failingRandomReader struct{}

func (failingRandomReader) Read([]byte) (int, error) {
	return 0, errors.New("test entropy failure")
}

func TestSecureRandomHexRejectsShortReadAndError(t *testing.T) {
	for _, reader := range []io.Reader{
		bytes.NewReader(make([]byte, 31)),
		failingRandomReader{},
	} {
		restore := replaceSecureRandomReaderForTest(reader)
		_, err := secureRandomHex(32)
		restore()
		if err == nil {
			t.Fatal("secureRandomHex() accepted incomplete entropy")
		}
	}
}

func TestSecureRandomHexConcurrentValuesAreUnique(t *testing.T) {
	const goroutines = 16
	const perGoroutine = 128
	values := make(chan string, goroutines*perGoroutine)
	errorsFound := make(chan error, goroutines)

	var workers sync.WaitGroup
	workers.Add(goroutines)
	for range goroutines {
		go func() {
			defer workers.Done()
			for range perGoroutine {
				value, err := secureRandomHex(32)
				if err != nil {
					errorsFound <- err
					return
				}
				values <- value
			}
		}()
	}
	workers.Wait()
	close(values)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("secureRandomHex() error = %v", err)
	}

	seen := make(map[string]struct{}, goroutines*perGoroutine)
	for value := range values {
		if len(value) != 64 {
			t.Fatalf("secureRandomHex() length = %d, want 64", len(value))
		}
		if _, exists := seen[value]; exists {
			t.Fatal("secureRandomHex() returned a duplicate value")
		}
		seen[value] = struct{}{}
	}
	if len(seen) != goroutines*perGoroutine {
		t.Fatalf("unique values = %d, want %d", len(seen), goroutines*perGoroutine)
	}
}
