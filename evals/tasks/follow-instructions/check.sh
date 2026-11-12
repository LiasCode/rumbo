# The function works, and it follows the three project rules that apply: the
# error prefix, the doc comment, and a test of its own.
set -e
go vet ./...
grep -q -E '^// Remove ' inventory.go || { echo "Remove has no doc comment starting with its name"; exit 1; }
grep -q -E 'Remove\(' inventory_test.go || { echo "inventory_test.go does not test Remove"; exit 1; }
git diff --quiet eval-base -- go.mod || { echo "go.mod was modified"; exit 1; }
cp "$RUMBO_EVAL_TASK/hidden_test.go.txt" hidden_test.go
trap 'rm -f hidden_test.go' EXIT
go test ./...
