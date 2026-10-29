// Package mediaformat holds format limits and failures shared by import and transport.
package mediaformat

import "errors"

const MaxPreviewBytes = 1 << 20
const MaxAudiobookBytes = 256 << 20

var ErrUnsupported = errors.New("media: required audio tools are unavailable")
