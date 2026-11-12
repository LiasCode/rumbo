package agent

// defaultCompactAt is the most a conversation grows before it is summarized
// when nothing says otherwise. Models with very large contexts are held to
// it too: a context that large costs more on every request and is not read
// more carefully for it.
const defaultCompactAt = 150_000

// defaultCompactShare is how much of a smaller model's context is used
// before summarizing, in percent. The rest is room for the next step.
const defaultCompactShare = 80

// Compaction says when a conversation is replaced by a summary. The zero
// value is the default: at defaultCompactShare of the model's context, and
// never later than defaultCompactAt.
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
	switch {
	case c.Never:
		return 0

	case c.Tokens > 0:
		return c.Tokens

	case c.Percent > 0 && window > 0:
		return window * int64(c.Percent) / 100
	}

	if window > 0 {
		return min(defaultCompactAt, window*defaultCompactShare/100)
	}

	return defaultCompactAt
}
