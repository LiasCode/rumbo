# The bug only shows in what the program prints, so the agent has to run it
# and compare, in a language other than the rest of the set.
set -e
git diff --quiet eval-base -- sales.csv || { echo "sales.csv was modified"; exit 1; }
expected="$(printf 'east 7.00\nnorth 12.75\nsouth 5.00')"
actual="$(python3 report.py)"
[ "$actual" = "$expected" ] || { echo "wrong output:"; echo "$actual"; exit 1; }
