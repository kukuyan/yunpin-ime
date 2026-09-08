// SPDX-License-Identifier: Apache-2.0
package desktopagent

import "errors"

// ConfigureDefaultLearningSource is shared by the resident and explicit
// sync-once. A manual round must not count the native event for an action that
// the resident already observed in Rime's cumulative userdb. This only wires
// the fixed maintenance hook; it performs no I/O or maintenance itself.
func ConfigureDefaultLearningSource(agent *Agent, defaults Paths) error {
	if agent == nil || agent.Profile != DefaultProfile ||
		agent.StateDirectory != defaults.StateDirectory || agent.DatabasePath != defaults.DatabasePath ||
		agent.NativeEventsPath != defaults.NativeEventsPath || agent.BaselinePath != defaults.BaselinePath ||
		agent.SnapshotPath != defaults.SnapshotPath || agent.SnapshotStatePath != defaults.SnapshotStatePath {
		return errors.New("custom sync state requires an explicit Rime userdb export; automatic maintenance uses only fixed platform paths")
	}
	paths, err := DefaultRimeBridgePaths(defaults)
	if err != nil {
		return err
	}
	refresh, err := NewDefaultRimeUserDBRefresh(paths)
	if err != nil {
		return err
	}
	agent.RimeUserDBExportPath = paths.StagingPath
	agent.RimeUserDBRefresh = refresh
	return nil
}
