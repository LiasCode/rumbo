# The signature changed, every caller in other files and packages was
# updated, and the behaviour holds against a test the agent never saw.
set -e
go vet ./...
cp "$RUMBO_EVAL_TASK/hidden_test.go.txt" hidden_test.go
trap 'rm -f hidden_test.go' EXIT
go test ./...
