package agent

// defaultCompactShare is how much of the model's context is used before
// summarizing, in percent, when nothing says otherwise. The rest is room
// for the next step.
const defaultCompactShare = 80

// unsizedCompactAt is the size a conversation is summarized at when the
// provider does not say how large the model's context is.
const unsizedCompactAt = 150_000

// Compaction says when a conversation is replaced by a summary. The zero
// value is the default: at defaultCompactShare of the model's context.
type Compaction struct {
	// Tokens is a fixed size to compact at.
	Tokens int64
	// Percent is a share of the model's context to compact at. It needs the
	// provider to report the context's size.
	Percent int
	// Never turns compaction off.
	Never bool
}

// Limit returns the context size to compact at, given the size of the
// model's context, which is zero when the provider did not say. Zero means
// never.
func (c Compaction) Limit(window int64) int64 {
	share := int64(defaultCompactShare)

	switch {
	case c.Never:
		return 0

	case c.Tokens > 0:
		return c.Tokens

	case c.Percent > 0:
		share = int64(c.Percent)
	}

	if window > 0 {
		return window * share / 100
	}

	return unsizedCompactAt
}
