// Package events records observations, never inputs to call admission.
package observation

import (
	"time"

	"callmemaybe/internal/calls"
)

// Type is a closed vocabulary: arbitrary ARI messages and log arguments must
// never become a way to persist credentials or entered digits.
type Type string

const (
	JournalNote           Type = "journal.note"
	ChannelStarted        Type = "channel.started"
	ChannelAnswered       Type = "channel.answered"
	ChannelHungup         Type = "channel.hungup"
	ChannelEnded          Type = "channel.ended"
	ChannelBridgeEntered  Type = "channel.bridge_entered"
	ChannelBridgeExited   Type = "channel.bridge_exited"
	ChannelTransfer       Type = "channel.transfer"
	ChannelLinkedEnded    Type = "channel.linked_ended"
	ChannelDialStarted    Type = "channel.dial_started"
	ChannelApplication    Type = "channel.application"
	CallFinished          Type = "call.finished"
	CallObserved          Type = "call.observed"
	AdmissionDecided      Type = "admission.decided"
	RingStarted           Type = "ring.started"
	RingStageFinished     Type = "ring.stage_finished"
	CallAnswered          Type = "call.answered"
	CallHandedOff         Type = "call.handed_off"
	SessionFinished       Type = "session.finished"
	ConfigReloaded        Type = "config.reloaded"
	ConfigReloadFailed    Type = "config.reload_failed"
	ContactsRefreshed     Type = "contacts.refreshed"
	ContactsRefreshFailed Type = "contacts.refresh_failed"
	ARIConnected          Type = "ari.connected"
	ARIDisconnected       Type = "ari.disconnected"
	DaemonStarted         Type = "daemon.started"
	DaemonStopping        Type = "daemon.stopping"
	CoverageGap           Type = "journal.coverage_gap"
)

func Types() []Type {
	return append([]Type(nil), eventTypes[:]...)
}

var eventTypes = [...]Type{JournalNote, ChannelStarted, ChannelAnswered, ChannelHungup, ChannelEnded, ChannelBridgeEntered, ChannelBridgeExited, ChannelTransfer, ChannelLinkedEnded, ChannelDialStarted, ChannelApplication, CallFinished, CallObserved, AdmissionDecided, RingStarted, RingStageFinished, CallAnswered, CallHandedOff, SessionFinished, ConfigReloaded, ConfigReloadFailed, ContactsRefreshed, ContactsRefreshFailed, ARIConnected, ARIDisconnected, DaemonStarted, DaemonStopping, CoverageGap}

func ValidType(t Type) bool {
	for _, v := range eventTypes {
		if v == t {
			return true
		}
	}
	return false
}

// Payload deliberately has no arbitrary map or error string. Record can contain
// final dialled numbers but never console input, DTMF, or extension credentials.
type Payload struct {
	Channel *ChannelObservation `json:"channel,omitempty"`
	Record  *calls.Record       `json:"record,omitempty"`
	Reason  string              `json:"reason,omitempty"`
	Count   int64               `json:"count,omitempty"`
}
type Event struct {
	Sequence   int64     `json:"sequence,string"`
	ID         string    `json:"event_id"`
	Type       Type      `json:"type"`
	Version    int       `json:"version"`
	At         time.Time `json:"occurred_at"`
	RecordedAt time.Time `json:"recorded_at"`
	Source     string    `json:"source"`
	CallID     string    `json:"call_id,omitempty"`
	Line       string    `json:"line,omitempty"`
	Payload    Payload   `json:"payload"`
}
type Sink interface{ Post(Event) }

func System(t Type, reason string, count int64) Event {
	return Event{Type: t, At: time.Now().UTC(), Source: "doorman", Payload: Payload{Reason: reason, Count: count}}
}
func Call(t Type, channelID string, r calls.Record, reason string) Event {
	// Outcome is a completed-session summary. Before that, its initial
	// "abandoned" value is only a default and must not pretend to be a fact.
	if t != SessionFinished && t != CallHandedOff && t != CallFinished {
		r.Outcome = ""
		r.MS = 0
	}
	// The writer runs concurrently with the next ring stage; copy mutable slices.
	r.Stages = append([]calls.Stage(nil), r.Stages...)
	for i := range r.Stages {
		r.Stages[i].Handsets = append([]string(nil), r.Stages[i].Handsets...)
	}
	return Event{Type: t, At: time.Now().UTC(), Source: "doorman", CallID: channelID, Line: r.Line, Payload: Payload{Record: &r, Reason: reason}}
}

type ChannelObservation struct {
	SourceID       string `json:"source_id"`
	SourceSequence int64  `json:"source_sequence,string"`
	LinkedID       string `json:"linked_id"`
	Direction      string `json:"direction,omitempty"`
	Caller         string `json:"caller,omitempty"`
	Dialled        string `json:"dialled,omitempty"`
}
