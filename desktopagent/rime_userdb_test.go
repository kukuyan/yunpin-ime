// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kukuyan/yunpin-ime/protocol"
)

func TestParseRimeUserDBSnapshotStrictFormat(t *testing.T) {
	contents := []byte("# Rime user dictionary\n" +
		"#@/db_name\tyunpin\n" +
		"shu ju ku \t数据库\tc=7 d=3.5 t=9\n" +
		"si ren ci \t个人静态词\tc=12 d=1 t=10\n" +
		"shan chu \t删除标记\tc=-3 d=0 t=11\n")
	localOnly := map[string]struct{}{protocol.CanonicalPhrase("个人静态词"): {}}
	observations, ignored, err := parseRimeUserDBExportBytes(contents, localOnly)
	if err != nil {
		t.Fatal(err)
	}
	if ignored != 0 {
		t.Fatalf("strict Pinyin snapshot unexpectedly ignored %d rows", ignored)
	}
	if len(observations) != 3 || observations[0].Phrase.Pinyin != "shu ju ku" ||
		observations[0].Commits != 7 || observations[0].LocalOnly ||
		!observations[1].LocalOnly || observations[1].Commits != 12 ||
		observations[2].Commits != 0 {
		t.Fatalf("unexpected strict snapshot parse: %#v", observations)
	}
}

func TestParseRimeUserDBSnapshotRejectsMalformedRowsWithoutEcho(t *testing.T) {
	tests := map[string]string{
		"ordinary table export":         "数据库\tshu ju ku\t7\n",
		"extra field":                   "shu ju ku \t数据库\tc=7 d=1 t=9\textra\n",
		"metadata order":                "shu ju ku \t数据库\td=1 c=7 t=9\n",
		"metadata spacing":              "shu ju ku \t数据库\tc=7  d=1 t=9\n",
		"noncanonical commits":          "shu ju ku \t数据库\tc=01 d=1 t=9\n",
		"invalid dynamic score":         "shu ju ku \t数据库\tc=7 d=NaN t=9\n",
		"noncanonical tick":             "shu ju ku \t数据库\tc=7 d=1 t=09\n",
		"leading apostrophe":            "'shu ju ku \t数据库\tc=7 d=1 t=9\n",
		"repeated separator":            "shu  ju ku \t数据库\tc=7 d=1 t=9\n",
		"control phrase":                "shu ju ku \t数据库\u0001\tc=7 d=1 t=9\n",
		"non-Pinyin invalid phrase":     "a Y \t本地行\u0001\tc=2 d=1 t=8\n",
		"non-Pinyin invalid metadata":   "a Y \t本地行\tc=02 d=1 t=8\n",
		"non-Pinyin control suffix":     "a Y\u0001 \t本地行\tc=2 d=1 t=8\n",
		"non-Pinyin trailing separator": "Y' \t本地行\tc=2 d=1 t=8\n",
		"non-Pinyin repeated separator": "a  Y \t本地行\tc=2 d=1 t=8\n",
		"non-Pinyin non-ASCII code":     "a 码 \t本地行\tc=2 d=1 t=8\n",
		"duplicate identity": "shu ju ku \t数据库\tc=7 d=1 t=9\n" +
			"shu'ju'ku \t 数据 库 \tc=8 d=1 t=10\n",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := parseRimeUserDBExportBytes([]byte(contents), nil)
			if err == nil {
				t.Fatal("malformed Rime userdb row was accepted")
			}
			if strings.Contains(err.Error(), "数据库") {
				t.Fatalf("parser error echoed private phrase text: %v", err)
			}
		})
	}
	tooLong := "a \t词\tc=1 d=1 t=1" + strings.Repeat("x", maxRimeUserDBLineBytes) + "\n"
	if _, _, err := parseRimeUserDBExportBytes([]byte(tooLong), nil); err == nil {
		t.Fatal("oversized Rime userdb row was accepted")
	}
	if _, _, err := parseRimeUserDBExportBytes([]byte{0xff}, nil); err == nil {
		t.Fatal("invalid UTF-8 Rime userdb export was accepted")
	}
}

func TestParseRimeUserDBSnapshotIgnoresValidatedNonPinyinRows(t *testing.T) {
	contents := []byte("a Y \t本地非拼音行\tc=2 d=1 t=8\n" +
		"a-1 Y \t本地符号编码\tc=3 d=1 t=9\n" +
		"shu ju ku \t数据库\tc=7 d=1 t=9\n")
	observations, ignored, err := parseRimeUserDBExportBytes(contents, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ignored != 2 || len(observations) != 1 || observations[0].Phrase.Pinyin != "shu ju ku" {
		t.Fatalf("non-Pinyin filtering mismatch: ignored=%d observations=%#v", ignored, observations)
	}
}

func TestIngestRimeUserDBSnapshotIsPrivateAtomicAndIdempotent(t *testing.T) {
	store := openBridgeStore(t)
	directory := filepath.Join(t.TempDir(), "rime-userdb")
	makePrivateTestDirectory(t, directory)
	path := filepath.Join(directory, "yunpin.userdb.txt")
	contents := []byte("xue xi ci \t学习词\tc=4 d=2 t=9\n" +
		"si ren ci \t个人静态词\tc=20 d=8 t=10\n")
	writePrivateTestFile(t, path, contents)
	localOnly := map[string]struct{}{protocol.CanonicalPhrase("个人静态词"): {}}
	result, err := ingestRimeUserDBExport(context.Background(), path, store, localOnly)
	if err != nil || result.Rows != 2 || result.Advanced != 1 || result.LocalOnly != 1 {
		t.Fatalf("Rime snapshot ingest=%#v err=%v", result, err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil || len(snapshot.Phrases) != 1 || snapshot.Phrases[0].Text != "学习词" ||
		snapshot.Phrases[0].UseCount != 4 {
		t.Fatalf("Rime snapshot materialization mismatch: snapshot=%#v err=%v", snapshot, err)
	}
	if pending, err := store.PendingEventCount(context.Background()); err != nil || pending != 1 {
		t.Fatalf("Rime snapshot did not produce exactly one eligible outbox row: pending=%d err=%v", pending, err)
	}
	result, err = ingestRimeUserDBExport(context.Background(), path, store, localOnly)
	if err != nil || result.Advanced != 0 || result.LocalOnly != 0 || result.Resets != 0 {
		t.Fatalf("identical Rime snapshot replay was not idempotent: result=%#v err=%v", result, err)
	}
}

func TestRimeUserDBSourceSuppressesPerSelectionNativeSource(t *testing.T) {
	if (Agent{NativeEventsPath: "/private/native", RimeUserDBExportPath: "/private/userdb"}).nativeEventIngestionEnabled() {
		t.Fatal("Rime cumulative and native per-selection sources would be double-counted")
	}
	if !(Agent{NativeEventsPath: "/private/native"}).nativeEventIngestionEnabled() {
		t.Fatal("native source was disabled when no cumulative Rime source was selected")
	}
}
