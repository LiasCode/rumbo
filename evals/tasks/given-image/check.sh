# A file given by its path is looked at: the answer is what only the image
# shows, and the project is untouched.
set -e
grep -q 'KX-4827-PLUM' "$RUMBO_EVAL_OUTPUT" || { echo "the answer is missing or wrong:"; cat "$RUMBO_EVAL_OUTPUT"; exit 1; }
changes="$( { git diff --name-only eval-base; git ls-files --others --exclude-standard; } )"
[ -z "$changes" ] || { echo "files changed: $changes"; exit 1; }
