# The flag works, and the default output did not change.
set -e
binary="$(mktemp -d)/wordcount"
go build -o "$binary" .

plain="$(printf 'one two\nthree\n' | "$binary")"
[ "$plain" = "3" ] || { echo "default output changed: $plain"; exit 1; }

printf 'one two\nthree\n' | "$binary" -json | python3 -c '
import json, sys
data = json.load(sys.stdin)
assert data["words"] == 3 and data["lines"] == 2, data
'
