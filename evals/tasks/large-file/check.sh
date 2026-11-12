# The one line changed, and the other 3999 are exactly as they were.
set -e
grep -q -x 'request_timeout = 45' settings.ini || { echo "request_timeout is not 45"; exit 1; }
[ "$(wc -l < settings.ini)" -eq 4000 ] || { echo "settings.ini no longer has 4000 lines"; exit 1; }
stat="$(git diff eval-base --numstat -- settings.ini)"
[ "$stat" = "1	1	settings.ini" ] || { echo "expected one changed line, got: $stat"; exit 1; }
