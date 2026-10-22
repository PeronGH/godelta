package godelta

import (
	"errors"
	"strconv"
	"strings"
)

// Sentinel errors. Errors returned by this package wrap one of them and can be
// matched with errors.Is; use errors.As with *Error for details.
var (
	// ErrUnsupported reports a valid delta that uses a feature this package
	// does not implement, such as DJW secondary compression.
	ErrUnsupported = errors.New("godelta: unsupported feature")
	// ErrCorrupt reports malformed or truncated delta input.
	ErrCorrupt = errors.New("godelta: corrupt delta")
	// ErrChecksum reports a target window whose Adler-32 does not match.
	ErrChecksum = errors.New("godelta: checksum mismatch")
	// ErrSourceRange reports a copy outside the provided source, including
	// a source that is shorter than the delta requires.
	ErrSourceRange = errors.New("godelta: copy outside source")
	// ErrLimit reports a delta that exceeds a configured limit in Options.
	ErrLimit = errors.New("godelta: limit exceeded")
)

// Error describes a decoding failure.
type Error struct {
	// Kind is one of the sentinel errors above.
	Kind error
	// Feature names the unsupported feature when Kind is ErrUnsupported.
	Feature string
	// Window is the zero-based index of the window being processed, or -1
	// when the failure is in the file header.
	Window int64
	// Offset is the delta byte offset at which the failure was detected.
	Offset int64
	// Msg describes the failure.
	Msg string
	// Err is the underlying cause, if any.
	Err error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Kind.Error())
	if e.Feature != "" {
		b.WriteString(": ")
		b.WriteString(e.Feature)
	}
	if e.Window >= 0 {
		b.WriteString(" (window ")
		b.WriteString(strconv.FormatInt(e.Window, 10))
		b.WriteString(", delta offset ")
	} else {
		b.WriteString(" (header, delta offset ")
	}
	b.WriteString(strconv.FormatInt(e.Offset, 10))
	b.WriteString(")")
	if e.Msg != "" {
		b.WriteString(": ")
		b.WriteString(e.Msg)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() []error {
	if e.Err == nil {
		return []error{e.Kind}
	}
	return []error{e.Kind, e.Err}
}
