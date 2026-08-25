// SPDX-License-Identifier: Apache-2.0
package syncclient

// NetworkError marks a transport-layer failure. Its wrapped detail remains
// available to an interactive caller, but desktop health persists only the
// stable class "network".
type NetworkError struct {
	Err error
}

func (err *NetworkError) Error() string { return "sync network operation failed: " + err.Err.Error() }
func (err *NetworkError) Unwrap() error { return err.Err }

// RelayProtocolError marks a syntactically valid connection whose response
// violated the YunPin relay contract. It is deliberately distinct from an
// authentication rejection and from local database failures.
type RelayProtocolError struct {
	Err error
}

func (err *RelayProtocolError) Error() string {
	return "sync relay protocol failure: " + err.Err.Error()
}
func (err *RelayProtocolError) Unwrap() error { return err.Err }

// LocalStoreError marks a failure while reading or committing the encrypted
// local sync store.
type LocalStoreError struct {
	Err error
}

func (err *LocalStoreError) Error() string { return "sync local store failure: " + err.Err.Error() }
func (err *LocalStoreError) Unwrap() error { return err.Err }

func networkError(err error) error {
	if err == nil {
		return nil
	}
	return &NetworkError{Err: err}
}

func relayProtocolError(err error) error {
	if err == nil {
		return nil
	}
	return &RelayProtocolError{Err: err}
}

func localStoreError(err error) error {
	if err == nil {
		return nil
	}
	return &LocalStoreError{Err: err}
}
