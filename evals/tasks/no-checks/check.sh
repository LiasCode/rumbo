# The harness finds no checks in this project, so nothing sends the agent
# back: it has to verify the output on its own.
set -e
git diff --quiet eval-base -- sales.csv || { echo "sales.csv was modified"; exit 1; }
expected="$(printf 'east 7.00\nnorth 12.75\nsouth 5.00')"
actual="$(python3 report.py)"
[ "$actual" = "$expected" ] || { echo "wrong output:"; echo "$actual"; exit 1; }
