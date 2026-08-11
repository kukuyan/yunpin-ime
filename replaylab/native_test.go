// SPDX-License-Identifier: Apache-2.0
package replaylab

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeFrameStrictDecodeAndConversion(t *testing.T) {
	frame := syntheticNativeFrame(EventComposition)
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeNativeFrameV1(encoded)
	if err != nil {
		t.Fatalf("decode native frame: %v", err)
	}
	converter, err := NewNativeEventConverter(testSession)
	if err != nil {
		t.Fatal(err)
	}
	events, err := converter.Convert(decoded)
	if err != nil {
		t.Fatalf("convert native frame: %v", err)
	}
	if len(events) != 2 || events[0].Type != EventResume || events[1].Type != EventComposition {
		t.Fatalf("unexpected converted sequence: %#v", events)
	}

	unknown := append(encoded[:len(encoded)-1], []byte(`,"unexpected":true}`)...)
	if _, err := DecodeNativeFrameV1(unknown); err == nil {
		t.Fatal("unknown native frame field was accepted")
	}
}

func TestStoreReportPrefersActualNativeSpool(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lab")
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store, err := Init(root, start)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Start(start)
	if err != nil {
		t.Fatal(err)
	}
	frames := []NativeFrameV1{
		syntheticNativeFrame(EventComposition),
		syntheticNativeFrame(EventSelect),
		syntheticNativeFrame(EventBackspace),
		syntheticNativeFrame(EventCommit),
	}
	frames[1].Selection = &SelectionV1{Rank: 2, Text: "synthetic exact"}
	frames[2].EditCount = 1
	frames[3].FinalText = "synthetic exact"
	var spool []byte
	for index := range frames {
		frames[index].MonotonicUS += uint64(index)
		frames[index].UTCUnixUS += uint64(index)
		line, err := json.Marshal(frames[index])
		if err != nil {
			t.Fatal(err)
		}
		spool = append(spool, line...)
		spool = append(spool, '\n')
	}
	nativeDir := filepath.Join(root, "native")
	if err := os.MkdirAll(nativeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(nativeDir, metadata.SessionID+".native.yunpinreplay")
	if err := os.WriteFile(path, spool, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := store.Report()
	if err != nil {
		t.Fatalf("native report: %v", err)
	}
	if report.NativeEventCount != 4 || report.ExactPathCorrectionFirstCount != 1 {
		t.Fatalf("native evidence missing from report: %+v", report)
	}
	if report.BackspaceCount != 1 || report.CommitCount != 1 {
		t.Fatalf("native edit/commit metrics missing: %+v", report)
	}
}

func syntheticNativeFrame(kind EventType) NativeFrameV1 {
	return NativeFrameV1{
		Version: NativeEventVersionV1, MonotonicUS: 100,
		UTCUnixUS: 1_767_225_600_000_000, Type: kind,
		Composition: &CompositionV1{
			RawInput: "synthetic", NormalizedPinyin: "synthetic",
			CaretByte: len("synthetic"), ExactPathAvailable: true,
			Candidates: []CandidateV1{
				{Text: "synthetic correction", IsCorrection: true, Highlighted: true},
				{Text: "synthetic exact"},
			},
		},
	}
}
