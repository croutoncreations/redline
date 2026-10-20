package artifacts

import (
	"io"
	"os"
)

// ReadFileTail returns up to limit trailing bytes of the file at path. Unlike
// Reader.ReadTail it performs no root confinement, UTF-8 trimming, or
// regular-file check; it is for Redline's own run output files, where callers
// treat any error as "no evidence available".
func ReadFileTail(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := max(int64(0), info.Size()-limit)
	data := make([]byte, info.Size()-offset)
	_, err = file.ReadAt(data, offset)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return data, nil
}
