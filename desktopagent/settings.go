// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"unicode/utf8"
)

const maxRimeSettingsBytes = 1 << 20

// GuardSettings is the deliberately small set of ranking controls exposed by
// the user-facing settings page. Account, device, endpoint and recovery state
// have no representation here.
type GuardSettings struct {
	ShortInputGuard     bool `json:"short_input_guard"`
	LongCorrectionGuard bool `json:"long_correction_guard"`
	TypoCorrection      bool `json:"typo_correction"`
}

type GuardSettingsApplyResult struct {
	Changed  bool `json:"changed"`
	Reloaded bool `json:"reloaded"`
}

type guardSettingSpec struct {
	key   string
	value func(GuardSettings) bool
}

var guardSettingSpecs = []guardSettingSpec{
	{key: "yunpin/short_input_guard", value: func(settings GuardSettings) bool { return settings.ShortInputGuard }},
	{key: "yunpin/long_correction_guard", value: func(settings GuardSettings) bool { return settings.LongCorrectionGuard }},
	{key: "yunpin/typo_correction", value: func(settings GuardSettings) bool { return settings.TypoCorrection }},
}

// RimeSettingsPath derives the fixed overlay beside the fixed YunPin snapshot
// root. The settings command does not accept an arbitrary path override.
func RimeSettingsPath(paths Paths) (string, error) {
	baseline := filepath.Clean(paths.BaselinePath)
	if paths.BaselinePath == "" || !filepath.IsAbs(baseline) ||
		filepath.Base(baseline) != "baseline.tsv" || filepath.Base(filepath.Dir(baseline)) != "yunpin" {
		return "", errors.New("fixed YunPin Rime baseline path is invalid")
	}
	return filepath.Join(filepath.Dir(filepath.Dir(baseline)), "rime_ice.custom.yaml"), nil
}

func guardSettingPattern(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^([ \t]*"` + regexp.QuoteMeta(key) + `"[ \t]*:[ \t]*)(true|false)([ \t]*(?:#[^\r\n]*)?\r?)$`)
}

func parseGuardSettings(contents []byte) (GuardSettings, error) {
	if !utf8.Valid(contents) || bytes.IndexByte(contents, 0) >= 0 {
		return GuardSettings{}, errors.New("Rime settings must be valid UTF-8 text")
	}
	var settings GuardSettings
	values := []*bool{&settings.ShortInputGuard, &settings.LongCorrectionGuard, &settings.TypoCorrection}
	for index, spec := range guardSettingSpecs {
		matches := guardSettingPattern(spec.key).FindAllSubmatch(contents, -1)
		if len(matches) != 1 {
			return GuardSettings{}, fmt.Errorf("Rime settings must contain exactly one boolean %s", spec.key)
		}
		*values[index] = bytes.Equal(matches[0][2], []byte("true"))
	}
	return settings, nil
}

func encodeGuardSettings(contents []byte, settings GuardSettings) ([]byte, bool, error) {
	if _, err := parseGuardSettings(contents); err != nil {
		return nil, false, err
	}
	updated := append([]byte(nil), contents...)
	changed := false
	for _, spec := range guardSettingSpecs {
		pattern := guardSettingPattern(spec.key)
		match := pattern.FindSubmatchIndex(updated)
		if len(match) < 8 {
			return nil, false, fmt.Errorf("Rime settings lost required boolean %s", spec.key)
		}
		wanted := []byte("false")
		if spec.value(settings) {
			wanted = []byte("true")
		}
		if bytes.Equal(updated[match[4]:match[5]], wanted) {
			continue
		}
		next := make([]byte, 0, len(updated)+len(wanted)-(match[5]-match[4]))
		next = append(next, updated[:match[4]]...)
		next = append(next, wanted...)
		next = append(next, updated[match[5]:]...)
		updated = next
		changed = true
	}
	return updated, changed, nil
}

func readRimeSettings(path string) ([]byte, os.FileInfo, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, nil, errors.New("Rime settings path must be absolute")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > maxRimeSettingsBytes {
		return nil, nil, errors.New("Rime settings must be a bounded regular file, not a symlink")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, nil, errors.New("Rime settings changed during validated open")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxRimeSettingsBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(contents) > maxRimeSettingsBytes {
		return nil, nil, errors.New("Rime settings exceed the size limit")
	}
	if _, err := parseGuardSettings(contents); err != nil {
		return nil, nil, err
	}
	return contents, before, nil
}

func writeRimeSettingsAtomic(path string, before os.FileInfo, contents []byte) error {
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("Rime settings parent must be a normal directory")
	}
	temporary, err := os.CreateTemp(parent, ".rime_ice.custom.yaml.*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	mode := before.Mode().Perm()
	if mode == 0 {
		mode = 0o600
	}
	if err = temporary.Chmod(mode); err == nil {
		_, err = temporary.Write(contents)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, current) || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 {
		return errors.New("Rime settings changed before atomic replacement")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace Rime settings: %w", err)
	}
	return syncParentDirectory(parent)
}

func LoadGuardSettings(path string) (GuardSettings, error) {
	contents, _, err := readRimeSettings(path)
	if err != nil {
		return GuardSettings{}, err
	}
	return parseGuardSettings(contents)
}

// ApplyGuardSettings updates only the three exact booleans above, preserving
// every other byte in the user's overlay. A deploy is requested even when the
// file already contains the selected values, so this button can also recover a
// host that has not yet loaded its on-disk configuration.
func ApplyGuardSettings(
	ctx context.Context, path string, settings GuardSettings, reload func(context.Context) error,
) (GuardSettingsApplyResult, error) {
	contents, before, err := readRimeSettings(path)
	if err != nil {
		return GuardSettingsApplyResult{}, err
	}
	updated, changed, err := encodeGuardSettings(contents, settings)
	if err != nil {
		return GuardSettingsApplyResult{}, err
	}
	result := GuardSettingsApplyResult{Changed: changed}
	if changed {
		if err := writeRimeSettingsAtomic(path, before, updated); err != nil {
			return result, err
		}
	}
	if reload == nil {
		return result, errors.New("Rime deploy hook is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := reload(ctx); err != nil {
		return result, fmt.Errorf("deploy updated Rime settings: %w", err)
	}
	result.Reloaded = true
	return result, nil
}
