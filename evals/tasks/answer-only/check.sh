# A question is answered, not acted on: the answer is right and the project
# is untouched.
set -e
grep -q 'teal-amber' "$RUMBO_EVAL_OUTPUT" || { echo "the answer is missing or wrong:"; cat "$RUMBO_EVAL_OUTPUT"; exit 1; }
changes="$( { git diff --name-only eval-base; git ls-files --others --exclude-standard; } )"
[ -z "$changes" ] || { echo "files changed: $changes"; exit 1; }
