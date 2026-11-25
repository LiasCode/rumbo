# A file given by its path that `read` does not show, a PDF, is still read
# by other means: the answer is what the document says, and the project is
# untouched.
set -e
grep -q '1,\{0,1\}556[.,]54' "$RUMBO_EVAL_OUTPUT" || { echo "the answer is missing or wrong:"; cat "$RUMBO_EVAL_OUTPUT"; exit 1; }
changes="$( { git diff --name-only eval-base; git ls-files --others --exclude-standard; } )"
[ -z "$changes" ] || { echo "files changed: $changes"; exit 1; }
