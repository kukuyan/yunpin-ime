// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package desktopagent

import (
	"context"

	"github.com/kukuyan/yunpin-ime/localstore"
)

func (agent Agent) runPlatformResident(ctx context.Context, bundle *CredentialBundleV1, options RunOptions) error {
	return runLoopWithResidentLock(ctx, func(roundContext context.Context) (SyncSummary, error) {
		return agent.syncOnceWithBundle(roundContext, bundle)
	}, options)
}

func recordPlatformSyncStart(context.Context, *localstore.Store) error        { return nil }
func recordPlatformNetworkCompleted(context.Context, *localstore.Store) error { return nil }
func finishPlatformSync(context.Context, *localstore.Store) error             { return nil }
