package events

import (
	"callmemaybe/internal/observation"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

type Type = observation.Type
type Event = observation.Event
type Payload = observation.Payload
type ChannelObservation = observation.ChannelObservation
type Sink = observation.Sink

const (
	JournalNote           = observation.JournalNote
	ChannelStarted        = observation.ChannelStarted
	ChannelAnswered       = observation.ChannelAnswered
	ChannelHungup         = observation.ChannelHungup
	ChannelEnded          = observation.ChannelEnded
	ChannelBridgeEntered  = observation.ChannelBridgeEntered
	ChannelBridgeExited   = observation.ChannelBridgeExited
	ChannelTransfer       = observation.ChannelTransfer
	ChannelLinkedEnded    = observation.ChannelLinkedEnded
	ChannelDialStarted    = observation.ChannelDialStarted
	ChannelApplication    = observation.ChannelApplication
	CallFinished          = observation.CallFinished
	CallObserved          = observation.CallObserved
	AdmissionDecided      = observation.AdmissionDecided
	RingStarted           = observation.RingStarted
	RingStageFinished     = observation.RingStageFinished
	CallAnswered          = observation.CallAnswered
	CallHandedOff         = observation.CallHandedOff
	SessionFinished       = observation.SessionFinished
	ConfigReloaded        = observation.ConfigReloaded
	ConfigReloadFailed    = observation.ConfigReloadFailed
	ContactsRefreshed     = observation.ContactsRefreshed
	ContactsRefreshFailed = observation.ContactsRefreshFailed
	ARIConnected          = observation.ARIConnected
	ARIDisconnected       = observation.ARIDisconnected
	DaemonStarted         = observation.DaemonStarted
	DaemonStopping        = observation.DaemonStopping
	CoverageGap           = observation.CoverageGap
)

var Types = observation.Types
var ValidType = observation.ValidType
var Call = observation.Call
var System = observation.System

func identity() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
