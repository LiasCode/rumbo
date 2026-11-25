//go:build unix

package tool

// shell is bash, which every Unix that rumbo runs on has.
var shell = shellProgram{
	name:    "bash",
	argv:    []string{"bash", "-c"},
	stopped: "kill -- -%d",
}
