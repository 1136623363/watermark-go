package server

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
)

var secureRandomState = struct {
	sync.Mutex
	reader io.Reader
}{reader: cryptorand.Reader}

func secureRandomBytes(size int) ([]byte, error) {
	if size <= 0 {
		return nil, errors.New("secure random size must be positive")
	}
	buffer := make([]byte, size)
	secureRandomState.Lock()
	_, err := io.ReadFull(secureRandomState.reader, buffer)
	secureRandomState.Unlock()
	if err != nil {
		return nil, err
	}
	return buffer, nil
}

func secureRandomHex(size int) (string, error) {
	buffer, err := secureRandomBytes(size)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func replaceSecureRandomReaderForTest(reader io.Reader) func() {
	if reader == nil {
		panic("secure random reader must not be nil")
	}
	secureRandomState.Lock()
	previous := secureRandomState.reader
	secureRandomState.reader = reader
	secureRandomState.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			secureRandomState.Lock()
			secureRandomState.reader = previous
			secureRandomState.Unlock()
		})
	}
}
