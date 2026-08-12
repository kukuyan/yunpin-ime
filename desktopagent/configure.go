// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/kukuyan/yunpin-ime/syncclient"
)

// ConfigureEndpoint atomically writes the only non-secret desktop sync
// configuration. Endpoint validation happens before the filesystem changes;
// bearer tokens and keys have no representation in this document.
func ConfigureEndpoint(path, endpoint string, allowPrivateHTTP bool) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("endpoint configuration path must be absolute")
	}
	policy := syncclient.EndpointPolicy{AllowPrivateHTTP: allowPrivateHTTP}
	if _, err := syncclient.ParseEndpoint(endpoint, policy); err != nil {
		return err
	}
	encoded, err := json.Marshal(syncclient.EndpointConfig{Endpoint: endpoint, EndpointPolicy: policy})
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	_, err = writeAtomicPrivateFile(path, encoded)
	return err
}
