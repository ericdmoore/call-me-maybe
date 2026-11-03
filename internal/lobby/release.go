package lobby

import "sync/atomic"

// channelRelease tracks ownership, not call cancellation. A requested release
// can fail; a StasisEnd confirms that the dialplan owns the channel forever.
// HTTP completion and StasisEnd race, so only a still-pending request may be
// rolled back. Otherwise cleanup could hang up a caller already in voicemail.
const (
	releaseOwned uint32 = iota
	releasePending
	releaseConfirmed
)

type channelRelease struct{ state atomic.Uint32 }

func (r *channelRelease) begin()         { r.state.CompareAndSwap(releaseOwned, releasePending) }
func (r *channelRelease) failed() bool   { return r.state.CompareAndSwap(releasePending, releaseOwned) }
func (r *channelRelease) confirmed()     { r.state.Store(releaseConfirmed) }
func (r *channelRelease) detached() bool { return r.state.Load() != releaseOwned }
