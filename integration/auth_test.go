// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"context"
	"testing"

	"github.com/kukuyan/yunpin-ime/syncclient"
)

func integrationUserSession(t *testing.T, ctx context.Context, endpoint syncclient.Endpoint) syncclient.UserSession {
	t.Helper()
	session, err := syncclient.New(endpoint).Register(ctx, "integration-user", "integration-password-which-is-long")
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func integrationAuthenticatedClient(t *testing.T, ctx context.Context, endpoint syncclient.Endpoint) *syncclient.Client {
	t.Helper()
	session := integrationUserSession(t, ctx, endpoint)
	return syncclient.New(endpoint, syncclient.WithUserSession(session.Token))
}
