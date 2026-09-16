package outboxcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/database"
	"github.com/lihongjie0209/go-api-template/internal/eventbus"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/spf13/cobra"
)

type commonOptions struct {
	configPath string
	profile    string
	actor      string
}

type listOptions struct {
	commonOptions
	page, pageSize int
	keyword        string
}

type replayOptions struct {
	commonOptions
	id      string
	version int64
}

type listRunner func(context.Context, listOptions) (eventbus.DeadPage, error)
type replayRunner func(context.Context, replayOptions) error

func NewCommand() *cobra.Command {
	return newCommand(runList, runReplay)
}

func newCommand(list listRunner, replay replayRunner) *cobra.Command {
	root := &cobra.Command{Use: "outboxctl", Short: "Inspect and repair this service's transactional outbox", SilenceUsage: true, SilenceErrors: true}
	var common commonOptions
	dead := &cobra.Command{Use: "dead", Short: "Manage exhausted outbox events"}
	dead.PersistentFlags().StringVar(&common.configPath, "config", "", "configuration file path (default: discover config.* in . or ./config)")
	dead.PersistentFlags().StringVar(&common.profile, "env", "", "active environment profile (overrides APP_ENV and config)")
	dead.PersistentFlags().StringVar(&common.actor, "actor", "", "bounded system actor ID recorded by database audit triggers")
	_ = dead.MarkPersistentFlagRequired("actor")

	listOptions := listOptions{}
	listCommand := &cobra.Command{
		Use:   "list",
		Short: "List dead events without exposing envelope payloads",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			listOptions.commonOptions = common
			result, err := list(command.Context(), listOptions)
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(result)
		},
	}
	listCommand.Flags().IntVar(&listOptions.page, "page", 1, "page number")
	listCommand.Flags().IntVar(&listOptions.pageSize, "page-size", 20, "page size, at most 100")
	listCommand.Flags().StringVar(&listOptions.keyword, "keyword", "", "match event ID, subject, or last error")

	replayOptions := replayOptions{}
	replayCommand := &cobra.Command{
		Use:   "replay",
		Short: "Replay one dead event using optimistic locking",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			replayOptions.commonOptions = common
			if err := replay(command.Context(), replayOptions); err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(map[string]any{"id": replayOptions.id, "replayed": true})
		},
	}
	replayCommand.Flags().StringVar(&replayOptions.id, "id", "", "dead event ID")
	replayCommand.Flags().Int64Var(&replayOptions.version, "version", 0, "expected event version")
	_ = replayCommand.MarkFlagRequired("id")
	_ = replayCommand.MarkFlagRequired("version")

	version := &cobra.Command{
		Use: "version", Short: "Print build version information", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(command.OutOrStdout(), "version=%s commit=%s build_time=%s\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime)
			return err
		},
	}
	dead.AddCommand(listCommand, replayCommand)
	root.AddCommand(dead, version)
	return root
}

func runList(ctx context.Context, options listOptions) (eventbus.DeadPage, error) {
	service, closeDatabase, actorCtx, err := openService(ctx, options.commonOptions)
	if err != nil {
		return eventbus.DeadPage{}, err
	}
	defer closeDatabase()
	return service.Dead(actorCtx, pagination.Request{Page: options.page, PageSize: options.pageSize, Keyword: options.keyword})
}

func runReplay(ctx context.Context, options replayOptions) error {
	service, closeDatabase, actorCtx, err := openService(ctx, options.commonOptions)
	if err != nil {
		return err
	}
	defer closeDatabase()
	return service.Replay(actorCtx, options.id, options.version)
}

func openService(ctx context.Context, options commonOptions) (*eventbus.RepairService, func(), context.Context, error) {
	if !validActor(options.actor) {
		return nil, nil, nil, eventbus.ErrRepairInvalid
	}
	cfg, err := config.LoadDatabaseWithProfile(options.configPath, options.profile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load database configuration: %w", err)
	}
	openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, err := database.Open(openCtx, cfg.Database)
	if err != nil {
		return nil, nil, nil, err
	}
	closeDatabase := func() { _ = db.Close() }
	actorCtx := platformprincipal.SystemContext(ctx, options.actor)
	return eventbus.NewRepairService(db, database.NewTransactor(db)), closeDatabase, actorCtx, nil
}

func validActor(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
