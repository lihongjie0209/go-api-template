package policyctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/eventbus"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/operationlog"
	"github.com/lihongjie0209/go-api-template/internal/permission"
	"github.com/lihongjie0209/go-api-template/internal/routepolicy"
	"github.com/lihongjie0209/go-api-template/internal/securitylog"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/spf13/cobra"
)

type bootstrapOptions struct {
	configPath string
	profile    string
	manifest   string
	actor      string
}

type bootstrapRunner func(context.Context, bootstrapOptions) (routepolicy.BootstrapResult, error)

func NewCommand() *cobra.Command {
	return newCommand(runBootstrap)
}

func newCommand(runner bootstrapRunner) *cobra.Command {
	root := &cobra.Command{Use: "policyctl", Short: "Manage deployment-time route authorization policies", SilenceUsage: true, SilenceErrors: true}
	var options bootstrapOptions
	bootstrap := &cobra.Command{
		Use:   "bootstrap",
		Short: "Idempotently apply a strict route-policy manifest",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := runner(command.Context(), options)
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(result)
		},
	}
	flags := bootstrap.Flags()
	flags.StringVar(&options.configPath, "config", "", "configuration file path (default: discover config.* in . or ./config)")
	flags.StringVar(&options.profile, "env", "", "active environment profile (overrides APP_ENV and config)")
	flags.StringVar(&options.manifest, "manifest", "", "strict YAML policy manifest")
	flags.StringVar(&options.actor, "actor", "", "bounded system actor ID recorded in audit fields")
	_ = bootstrap.MarkFlagRequired("manifest")
	_ = bootstrap.MarkFlagRequired("actor")
	version := &cobra.Command{
		Use:   "version",
		Short: "Print build version information",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(command.OutOrStdout(), "version=%s commit=%s build_time=%s\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime)
			return err
		},
	}
	root.AddCommand(bootstrap, version)
	return root
}

func runBootstrap(ctx context.Context, options bootstrapOptions) (routepolicy.BootstrapResult, error) {
	manifest, err := routepolicy.LoadBootstrapManifest(options.manifest)
	if err != nil {
		return routepolicy.BootstrapResult{}, err
	}
	cfg, err := config.LoadWithProfile(options.configPath, options.profile)
	if err != nil {
		return routepolicy.BootstrapResult{}, fmt.Errorf("load configuration: %w", err)
	}
	if !cfg.Database.Enabled || !cfg.EventBus.Enabled || !cfg.OperationLog.Enabled || !cfg.SecurityLog.Enabled {
		return routepolicy.BootstrapResult{}, errors.New("policy bootstrap requires database, event bus, operation log and security log")
	}
	openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, err := database.Open(openCtx, cfg.Database)
	if err != nil {
		return routepolicy.BootstrapResult{}, err
	}
	defer func() { _ = db.Close() }()
	bus, err := eventbus.New(openCtx, cfg)
	if err != nil {
		return routepolicy.BootstrapResult{}, fmt.Errorf("connect event bus: %w", err)
	}
	defer func() { _ = eventbus.Close(bus) }()
	logger := slog.Default()
	transactor := database.NewTransactor(db)
	metrics := observability.NewMetrics(cfg, db, nil)
	outbox := eventbus.NewOutbox(cfg, bus, transactor, metrics, logger)
	operations := operationlog.New(cfg, bus, db, transactor, metrics, outbox, nil)
	security := securitylog.New(cfg, bus, db, transactor, metrics, outbox, nil)
	compiler, err := routepolicy.NewCompiler()
	if err != nil {
		return routepolicy.BootstrapResult{}, err
	}
	permissionService := permission.New(permission.NewRepository(db), transactor, nil, operations, security, nil, logger)
	actorCtx := platformprincipal.SystemContext(ctx, options.actor)
	for index, definition := range manifest.PermissionDefinitions {
		var parentID *string
		if definition.ParentKey != "" {
			id, err := permission.SeedID(definition.ParentKey)
			if err != nil {
				return routepolicy.BootstrapResult{}, fmt.Errorf("permission definition %d parent: %w", index, err)
			}
			parentID = &id
		}
		_, _, err := permissionService.Seed(actorCtx, permission.Input{ParentID: parentID, Key: definition.Key, Name: definition.Name, NodeType: definition.NodeType, Resource: definition.Resource, Action: definition.Action, Description: definition.Description, SortOrder: definition.SortOrder, Status: definition.Status})
		if err != nil {
			return routepolicy.BootstrapResult{}, fmt.Errorf("seed permission definition %d: %w", index, err)
		}
	}
	service := routepolicy.NewService(db, transactor, compiler, nil, operations, security, logger)
	return routepolicy.Bootstrap(ctx, db, service, options.actor, manifest)
}
