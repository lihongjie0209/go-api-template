package pbaccli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/pbac"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/spf13/cobra"
)

type publishOptions struct {
	configPath string
	profile    string
	actor      string
	tenantID   string
	file       string
}

type publishRunner func(context.Context, publishOptions) (pbac.Publication, error)

func NewCommand() *cobra.Command { return newCommand(runPublish) }

func newCommand(publish publishRunner) *cobra.Command {
	root := &cobra.Command{Use: "pbacctl", Short: "Bootstrap this service's PBAC policy database", SilenceUsage: true, SilenceErrors: true}
	var options publishOptions
	actionPolicy := &cobra.Command{Use: "action-policy", Short: "Manage operation-authorization policies"}
	publishCommand := &cobra.Command{
		Use:   "publish",
		Short: "Create and publish the first immutable version of a policy",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			publication, err := publish(command.Context(), options)
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(publication)
		},
	}
	publishCommand.Flags().StringVar(&options.configPath, "config", "", "configuration file path (default: discover config.* in . or ./config)")
	publishCommand.Flags().StringVar(&options.profile, "env", "", "active environment profile (overrides APP_ENV and config)")
	publishCommand.Flags().StringVar(&options.actor, "actor", "", "bounded system actor ID recorded by database audit triggers")
	publishCommand.Flags().StringVar(&options.tenantID, "tenant-id", "", "tenant boundary required by a tenant policy")
	publishCommand.Flags().StringVar(&options.file, "file", "", "strict ActionPolicy YAML file")
	_ = publishCommand.MarkFlagRequired("actor")
	_ = publishCommand.MarkFlagRequired("file")
	actionPolicy.AddCommand(publishCommand)
	root.AddCommand(actionPolicy, &cobra.Command{
		Use: "version", Short: "Print build version information", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(command.OutOrStdout(), "version=%s commit=%s build_time=%s\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime)
			return err
		},
	})
	return root
}

func runPublish(ctx context.Context, options publishOptions) (pbac.Publication, error) {
	if !validIdentity(options.actor) || (options.tenantID != "" && !validIdentity(options.tenantID)) {
		return pbac.Publication{}, pbac.ErrInvalidPolicy
	}
	document, err := readPolicyFile(options.file)
	if err != nil {
		return pbac.Publication{}, fmt.Errorf("read policy file: %w", err)
	}
	policy, err := pbac.ParsePolicy(document)
	if err != nil {
		return pbac.Publication{}, err
	}
	if policy.Scope.Type == pbac.PolicyScopeTenant && policy.Scope.TenantID != options.tenantID {
		return pbac.Publication{}, fmt.Errorf("%w: --tenant-id must equal policy scope tenant_id", pbac.ErrInvalidPolicy)
	}
	if policy.Scope.Type == pbac.PolicyScopeGlobal && options.tenantID != "" {
		return pbac.Publication{}, fmt.Errorf("%w: global policy must not use --tenant-id", pbac.ErrInvalidPolicy)
	}
	cfg, err := config.LoadDatabaseWithProfile(options.configPath, options.profile)
	if err != nil {
		return pbac.Publication{}, fmt.Errorf("load database configuration: %w", err)
	}
	openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, err := database.Open(openCtx, cfg.Database)
	if err != nil {
		return pbac.Publication{}, err
	}
	defer func() { _ = db.Close() }()
	resources, err := pbac.NewRegistryFromDefinitions(pbac.PlatformResourceDefinitions())
	if err != nil {
		return pbac.Publication{}, err
	}
	repository := pbac.NewRepository(db)
	service := pbac.NewLifecycleService(repository, database.NewTransactor(db), resources, nil, nil, nil)
	actorCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: options.actor, Type: platformprincipal.TypeSystem, TenantID: options.tenantID})
	created, err := service.Create(actorCtx, pbac.CreatePolicyInput{Document: policy})
	if err != nil {
		return pbac.Publication{}, err
	}
	return service.Publish(actorCtx, pbac.PublishInput{PolicyID: created.Policy.ID, VersionNumber: created.Version.VersionNumber, ExpectedPolicyVersion: created.Policy.Version})
}

func readPolicyFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, pbac.MaxPolicyDocumentSize+1))
}

func validIdentity(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
