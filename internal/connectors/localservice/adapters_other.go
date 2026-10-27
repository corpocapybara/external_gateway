//go:build !windows

package localservice

import "errors"

func netAdapters() ([]netAdapter, error) {
	return nil, errors.New("listing network adapters is only supported on Windows")
}
