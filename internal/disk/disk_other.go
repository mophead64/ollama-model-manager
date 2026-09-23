//go:build !unix

package disk

import "errors"

func Stat(path string) (Usage, error) {
	return Usage{}, errors.New("disk usage not supported on this platform")
}
