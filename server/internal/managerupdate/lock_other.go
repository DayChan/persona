//go:build !unix

package managerupdate

import "errors"

func acquire(string) (func(), error) {
	return nil, errors.New("manager update requires Unix file locks")
}

func ownedIndependentFile(string, int) error {
	return errors.New("manager updates require Unix file identity")
}
