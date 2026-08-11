// SPDX-License-Identifier: Apache-2.0
package replaylab

import (
	"errors"
	"fmt"
	"time"
)

type SequenceState struct {
	SessionID       string
	LastEpisodeID   string
	LastSeq         uint64
	LastMonotonicUS uint64
	LastUTC         time.Time
	Paused          bool
	EpisodeClosed   bool
	SeenEpisodes    map[string]struct{}
}

// Accept validates stream ordering and mutates state only on success.
func (state *SequenceState) Accept(event EventV1) error {
	if err := event.Validate(); err != nil {
		return err
	}
	utc, _ := time.Parse(time.RFC3339Nano, event.UTC)
	next := *state
	if next.SessionID == "" {
		if event.Seq != 1 {
			return errors.New("the first event in a session must have seq 1")
		}
		if event.Type != EventResume {
			return errors.New("the first event in a session must be resume")
		}
		next.SessionID = event.SessionID
	} else {
		if event.SessionID != next.SessionID {
			return errors.New("event session_id does not match the active session")
		}
		if event.Seq != next.LastSeq+1 {
			return fmt.Errorf("event seq %d is not the expected %d", event.Seq, next.LastSeq+1)
		}
		if event.MonotonicUS < next.LastMonotonicUS {
			return errors.New("monotonic_us moved backwards")
		}
		if utc.Before(next.LastUTC) {
			return errors.New("utc moved backwards")
		}
	}

	newEpisode := next.LastEpisodeID != "" && event.EpisodeID != next.LastEpisodeID
	if next.LastEpisodeID == "" {
		newEpisode = true
	}
	if newEpisode && next.SeenEpisodes != nil {
		if _, reused := next.SeenEpisodes[event.EpisodeID]; reused {
			return errors.New("episode_id was already closed and cannot be reused")
		}
		if len(next.SeenEpisodes) >= MaxEpisodes {
			return fmt.Errorf("session exceeds the maximum %d episodes", MaxEpisodes)
		}
	}
	if newEpisode && next.LastEpisodeID != "" && !next.EpisodeClosed && !next.Paused {
		return errors.New("episode_id changed before commit, abort, or pause")
	}
	if newEpisode && next.LastEpisodeID != "" && event.Type != EventComposition && event.Type != EventResume {
		return errors.New("a new episode_id must start with composition_snapshot or resume")
	}
	if !newEpisode && next.EpisodeClosed && event.Type != EventPause && event.Type != EventDropCount {
		return errors.New("a closed episode_id cannot receive more input events")
	}
	if next.Paused && event.Type != EventResume && event.Type != EventDropCount {
		return errors.New("only resume or drop_count is accepted while paused")
	}

	switch event.Type {
	case EventResume:
		if next.LastSeq != 0 && !next.Paused {
			return errors.New("resume is only valid at session start or after pause")
		}
		if next.LastSeq != 0 && !newEpisode {
			return errors.New("resume after pause must start a new episode_id")
		}
		next.Paused = false
		next.EpisodeClosed = false
	case EventPause:
		if next.Paused {
			return errors.New("session is already paused")
		}
		next.Paused = true
		next.EpisodeClosed = true
	case EventCommit, EventAbort:
		next.EpisodeClosed = true
	case EventComposition, EventSelect, EventBackspace, EventDelete:
		if newEpisode {
			next.EpisodeClosed = false
		}
	case EventDropCount:
		// A producer may report loss without changing episode lifecycle.
	default:
		return fmt.Errorf("unsupported event type %q", event.Type)
	}

	next.LastEpisodeID = event.EpisodeID
	next.LastSeq = event.Seq
	next.LastMonotonicUS = event.MonotonicUS
	next.LastUTC = utc
	if newEpisode {
		if next.SeenEpisodes == nil {
			next.SeenEpisodes = make(map[string]struct{})
		}
		next.SeenEpisodes[event.EpisodeID] = struct{}{}
	}
	*state = next
	return nil
}
