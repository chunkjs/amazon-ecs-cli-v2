// Copyright 2019 Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os"

	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/aws/PRIVATE-amazon-ecs-archer/internal/pkg/archer"
	"github.com/aws/PRIVATE-amazon-ecs-archer/internal/pkg/store/ssm"
	"github.com/aws/PRIVATE-amazon-ecs-archer/internal/pkg/workspace"
	"github.com/spf13/cobra"
)

// InitProjectOpts contains the fields to collect for creating a project.
type InitProjectOpts struct {
	ProjectName string `survey:"project"`
	prompt      terminal.Stdio
	manager     archer.ProjectCreator
	ws          archer.Workspace
}

// Execute creates a new managed empty project.
func (opts *InitProjectOpts) Execute() error {
	if err := opts.manager.CreateProject(&archer.Project{
		Name:    opts.ProjectName,
		Version: "1.0",
	}); err != nil {
		return err
	}

	return opts.ws.Create(opts.ProjectName)
}

// BuildProjectInitCommand builds the command for creating a new project.
func BuildProjectInitCommand() *cobra.Command {
	opts := InitProjectOpts{
		prompt: terminal.Stdio{
			In:  os.Stdin,
			Out: os.Stderr,
			Err: os.Stderr,
		},
	}

	cmd := &cobra.Command{
		Use:   "init [name]",
		Short: "Creates a new, empty project",
		Example: `
  Create a new project named test
  $ archer project init test`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ssmStore, err := ssm.NewStore()
			if err != nil {
				return err
			}

			ws, err := workspace.New()
			if err != nil {
				return err
			}
			opts.ws = ws
			opts.manager = ssmStore
			opts.ProjectName = args[0]
			return opts.Execute()
		},
	}
	return cmd
}