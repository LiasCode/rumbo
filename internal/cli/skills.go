package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LiasCode/rumbo/internal/ambient"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
)

// This file tells a person which skills apply in a directory: those the
// model is told of, and those left out, with why. The command line and the
// host's function both go through it.

// describeSkills says what was found of the skills in a directory.
func describeSkills(skills ambient.Skills) string {
	var out strings.Builder

	if len(skills.Found) == 0 {
		out.WriteString("no skills here. A skill is a directory with a SKILL.md, in one of:\n")

		for _, place := range skills.Places {
			fmt.Fprintf(&out, "  %s\n", place)
		}
	} else {
		out.WriteString("the model is told of these skills:\n")

		for _, skill := range skills.Found {
			fmt.Fprintf(&out, "  %s: %s\n    %s\n", skill.Name, strings.Join(strings.Fields(skill.Description), " "), skill.Path)
		}
	}

	if len(skills.Left) > 0 {
		out.WriteString("left out:\n")

		for _, left := range skills.Left {
			fmt.Fprintf(&out, "  %s\n    %s\n", left.Path, left.Reason)
		}
	}

	return strings.TrimRight(out.String(), "\n")
}

// listSkills is the host's function: the skills of the current session's
// directory.
func (h *host) listSkills(ctx context.Context, _ map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	dir := h.current.dir
	h.mu.Unlock()

	plugins, err := skillPlaces(h.database)

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: describeSkills(ambient.FindSkills(ctx, dir, plugins))}, nil
}

// printSkills is the command: the skills of the working directory.
func printSkills(database store.Store, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: rumbo skills")
	}

	dir, _, err := workingProject()

	if err != nil {
		return err
	}

	plugins, err := skillPlaces(database)

	if err != nil {
		return err
	}

	fmt.Println(describeSkills(ambient.FindSkills(context.Background(), dir, plugins)))

	return nil
}
