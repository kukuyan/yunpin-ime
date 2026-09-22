// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var privateCandidateDeclarations = regexp.MustCompile(`(?m)^[ \t]*(?:"yunpin/enabled"|'yunpin/enabled'|yunpin/enabled)[ \t]*:`)
var privateFilterDeclarations = regexp.MustCompile(`(?m)^[ \t]*(?:"engine/filters/@before 0"|'engine/filters/@before 0'|engine/filters/@before 0)[ \t]*:`)
var privateFilterSetting = regexp.MustCompile(`(?m)^[ \t]*"engine/filters/@before 0"[ \t]*:[ \t]*yunpin_filter@yunpin[ \t]*(?:#[^\r\n]*)?\r?$`)

func parsePrivateCandidates(contents []byte) (bool, error) {
	if len(privateCandidateDeclarations.FindAllIndex(contents, -1)) != 1 {
		return false, errors.New("Rime overlay must contain one private candidate setting")
	}
	match := guardSettingPattern("yunpin/enabled").FindSubmatch(contents)
	if len(match) != 4 || len(privateFilterDeclarations.FindAllIndex(contents, -1)) != 1 || !privateFilterSetting.Match(contents) {
		return false, errors.New("Rime overlay does not contain the supported private candidate filter")
	}
	return bytes.Equal(match[2], []byte("true")), nil
}

func PrivateCandidatesEnabled(paths Paths) (bool, error) {
	path, err := RimeSettingsPath(paths)
	if err != nil {
		return false, err
	}
	contents, _, err := readRimeSettings(path)
	if err != nil {
		return false, err
	}
	return parsePrivateCandidates(contents)
}

// PreparePrivateVocabulary updates only the fixed Rime sync directory and
// private-candidate opt-in. It never turns a generated snapshot into baseline.
// The caller holds the normal agent lock, just as for existing guard settings.
func PreparePrivateVocabulary(ctx context.Context, paths Paths, reload func(context.Context) error) error {
	if reload == nil {
		return errors.New("private candidate deploy hook is unavailable")
	}
	path, err := RimeSettingsPath(paths)
	if err != nil {
		return err
	}
	contents, before, err := readRimeSettings(path)
	if err != nil {
		return err
	}
	enabled, err := parsePrivateCandidates(contents)
	if err != nil {
		return err
	}
	bridge, err := DefaultRimeBridgePaths(paths)
	if err != nil {
		return err
	}
	if err := ConfigureRimeBridge(bridge); err != nil {
		return err
	}
	// Every explicit prepare, including a retry with opt-in already saved,
	// requires a new sync receipt. The bridge or baseline may have changed.
	if _, err := writeAtomicPrivateFile(paths.SnapshotStatePath, []byte("settings-pending\n")); err != nil {
		return err
	}
	if enabled {
		// Explicit retry also retries the deploy, including a previous attempt
		// that saved the opt-in but was interrupted before native maintenance.
		return reload(context.WithValue(ctx, settingsDeployContextKey{}, true))
	}
	backup := filepath.Join(paths.StateDirectory, "onboarding-overlay-before.yaml")
	if err := ensurePrivateDirectory(paths.StateDirectory); err != nil {
		return err
	}
	// Retain the original overlay once. A later retry must never replace that
	// backup with a partially applied configuration.
	if _, err := readBoundedRegular(backup, maxRimeSettingsBytes); errors.Is(err, os.ErrNotExist) {
		if _, err := writeAtomicPrivateFile(backup, contents); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	match := guardSettingPattern("yunpin/enabled").FindSubmatchIndex(contents)
	updated := append([]byte(nil), contents[:match[4]]...)
	updated = append(updated, []byte("true")...)
	updated = append(updated, contents[match[5]:]...)
	if err := writeRimeSettingsAtomic(path, before, updated); err != nil {
		return err
	}
	if err := reload(context.WithValue(ctx, settingsDeployContextKey{}, true)); err != nil {
		return fmt.Errorf("deploy private candidate configuration: %w", err)
	}
	return nil
}
