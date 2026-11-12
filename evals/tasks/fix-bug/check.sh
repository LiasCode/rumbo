# The tests pass, and they pass because the code was fixed, not the tests.
set -e
git diff --quiet eval-base -- calc_test.go || { echo "calc_test.go was modified"; exit 1; }
go test ./...
