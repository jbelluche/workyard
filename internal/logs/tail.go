package logs

import (
	"bytes"
	"os"
	"strings"
)

func TailFile(path string, lines int, maxBytes int64) ([]string, bool, error) {
	if lines <= 0 {
		lines = 200
	}
	if maxBytes <= 0 {
		maxBytes = 128 * 1024
	}
	data, truncated, err := tailBytes(path, maxBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	parts := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(parts) == 1 && parts[0] == "" {
		return nil, truncated, nil
	}
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
		truncated = true
	}
	return parts, truncated, nil
}

func tailBytes(path string, maxBytes int64) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	size := stat.Size()
	readSize := size
	truncated := false
	if readSize > maxBytes {
		readSize = maxBytes
		truncated = true
	}
	buf := make([]byte, readSize)
	if readSize == 0 {
		return nil, false, nil
	}
	if _, err := f.ReadAt(buf, size-readSize); err != nil {
		return nil, false, err
	}
	if truncated {
		if idx := bytes.IndexByte(buf, '\n'); idx >= 0 && idx+1 < len(buf) {
			buf = buf[idx+1:]
		}
	}
	return buf, truncated, nil
}
