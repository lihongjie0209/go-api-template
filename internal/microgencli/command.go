package microgencli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/scaffold"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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
	var configFile string
	v := viper.New()
	command := &cobra.Command{
		Use:   "new",
		Short: "Generate an independent service from the template",
		Args:  cobra.NoArgs,
		PreRunE: func(command *cobra.Command, _ []string) error {
			if err := readConfig(v, configFile); err != nil {
				return err
			}
			options = scaffold.Options{
				Name: v.GetString("name"), Namespace: v.GetString("namespace"),
				MigrationTable: v.GetString("migration-table"), DatabaseName: v.GetString("database-name"), DatabaseSchema: v.GetString("database-schema"),
				Module: v.GetString("module"), Output: v.GetString("output"), Source: v.GetString("source"),
				Ref: v.GetString("ref"), Image: v.GetString("image"), BufModule: v.GetString("buf-module"),
				Description: v.GetString("description"), InitGit: v.GetBool("git-init"),
				Tidy: v.GetBool("tidy"), GenerateCode: v.GetBool("generate"),
			}
			return nil
		},
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
	flags.StringVar(&options.DatabaseName, "database-name", "", "service database name (default: normalized service name)")
	flags.StringVar(&options.DatabaseSchema, "database-schema", "", "PostgreSQL/Kingbase schema (default: normalized service name)")
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
	flags.StringVar(&configFile, "config", "", "config file (default: .microgen.yaml or microgen.yaml in current directory)")
	for _, key := range []string{"name", "namespace", "migration-table", "database-name", "database-schema", "module", "output", "source", "ref", "image", "buf-module", "description", "git-init", "tidy", "generate"} {
		if err := v.BindPFlag(key, flags.Lookup(key)); err != nil {
			panic(err)
		}
	}
	v.SetEnvPrefix("MICROGEN")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()
	return command
}

func readConfig(v *viper.Viper, explicit string) error {
	if explicit != "" {
		v.SetConfigFile(explicit)
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("read microgen config: %w", err)
		}
		return nil
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve current directory: %w", err)
	}
	for _, name := range []string{".microgen.yaml", ".microgen.yml", "microgen.yaml", "microgen.yml"} {
		candidate := filepath.Join(workingDirectory, name)
		if _, statErr := os.Stat(candidate); statErr == nil {
			v.SetConfigFile(candidate)
			if err := v.ReadInConfig(); err != nil {
				return fmt.Errorf("read microgen config: %w", err)
			}
			return nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect microgen config: %w", statErr)
		}
	}
	return nil
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
