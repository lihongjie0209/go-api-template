//go:build !linux

package outboxcli

import "os"

func openReplacementFile(path string) (*os.File, error) {
	return os.Open(path)
}
