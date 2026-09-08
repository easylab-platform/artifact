package artifactkit

import "errors"

// ErrProtocolUnknown is returned when cmd-artifact asks for a protocol that was not
// registered (i.e. no adapter package was blank-imported).
func ErrProtocolUnknown(name string) error {
	return errors.New("artifactkit: unknown protocol: " + name)
}
