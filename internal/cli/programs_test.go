package cli

import (
	"os"
	"slices"
	"testing"
)

// {self} stands for the rumbo executable wherever it is a word of a
// command: first, for a program rumbo serves itself, or as an argument,
// for a program that starts rumbo. Inside another word it is left alone.
func TestSelfIsTheRumboExecutableInACommand(t *testing.T) {
	self, err := os.Executable()

	if err != nil {
		t.Fatal(err)
	}

	for _, each := range []struct {
		command, want []string
	}{
		{[]string{selfPlaceholder, "provide", "openai"}, []string{self, "provide", "openai"}},
		{[]string{"window", "--rumbo", selfPlaceholder}, []string{"window", "--rumbo", self}},
		{[]string{"window", "--note={self}"}, []string{"window", "--note={self}"}},
		{nil, []string{}},
	} {
		resolved, err := programCommand(each.command)

		if err != nil || !slices.Equal(resolved, each.want) {
			t.Errorf("%v resolved to %v, err = %v; want %v", each.command, resolved, err, each.want)
		}
	}
}
