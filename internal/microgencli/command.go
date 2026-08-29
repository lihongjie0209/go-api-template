package microgencli

import (
	"fmt"

	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/scaffold"
	"github.com/spf13/cobra"
)

func NewCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "microgen",
		Short:         "Create a production-ready Go microservice",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newProjectCommand(), versionCommand())
	return root
}

func newProjectCommand() *cobra.Command {
	var options scaffold.Options
	command := &cobra.Command{
		Use:   "new",
		Short: "Generate an independent service from the template",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			output, err := scaffold.Generate(command.Context(), options)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(command.OutOrStdout(), output)
			return err
		},
	}
	flags := command.Flags()
	flags.StringVar(&options.Name, "name", "", "service name (lowercase DNS label)")
	flags.StringVar(&options.Namespace, "namespace", "", "Kubernetes namespace (default: service name)")
	flags.StringVar(&options.MigrationTable, "migration-table", "", "service-specific migration history table")
	flags.StringVar(&options.Module, "module", "", "Go module path")
	flags.StringVarP(&options.Output, "output", "o", "", "output directory (default: service name)")
	flags.StringVar(&options.Source, "source", "", "local template source instead of GitHub")
	flags.StringVar(&options.Ref, "ref", "main", "Git branch, tag, or commit to download")
	flags.StringVar(&options.Image, "image", "", "container image name (derived for GitHub modules)")
	flags.StringVar(&options.BufModule, "buf-module", "", "Buf module name (derived for GitHub modules)")
	flags.StringVar(&options.Description, "description", "", "service description")
	flags.BoolVar(&options.InitGit, "git-init", true, "initialize an independent Git repository")
	flags.BoolVar(&options.Tidy, "tidy", true, "run go mod tidy after generation")
	flags.BoolVar(&options.GenerateCode, "generate", true, "regenerate protobuf code with Buf")
	_ = command.MarkFlagRequired("name")
	_ = command.MarkFlagRequired("module")
	return command
}

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build version information",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(command.OutOrStdout(), "version=%s commit=%s build_time=%s\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime)
			return err
		},
	}
}
