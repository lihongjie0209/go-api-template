package scaffold

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

const (
	templateModule         = "github.com/lihongjie0209/go-api-template"
	templateName           = "go-api-template"
	templateImage          = "ghcr.io/lihongjie0209/go-api-template"
	templateBufModule      = "buf.build/lihongjie0209/go-api-template"
	templateNamespace      = "microservices"
	templateMigrationTable = "go_api_template_schema_migrations"
	templateDatabaseName   = "go_api_template_db"
	templateDatabaseSchema = "go_api_template"
	templateDescription    = "Production-oriented starter using Gin, Uber Fx, Viper, slog + lumberjack, sqlx, Redis, JWT, robfig/cron, and golang-migrate. Fork it or change the module path before starting a separate project."
	maxArchiveBytes        = 100 << 20
)

var serviceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
var migrationTablePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

type Options struct {
	Name           string
	Namespace      string
	MigrationTable string
	DatabaseName   string
	DatabaseSchema string
	Module         string
	Output         string
	Source         string
	Ref            string
	Image          string
	BufModule      string
	Description    string
	InitGit        bool
	Tidy           bool
	GenerateCode   bool
}

func (o *Options) Defaults() {
	if o.Output == "" {
		o.Output = o.Name
	}
	if o.Namespace == "" {
		o.Namespace = o.Name
	}
	if o.MigrationTable == "" {
		o.MigrationTable = strings.ReplaceAll(o.Name, "-", "_") + "_schema_migrations"
	}
	if o.DatabaseName == "" {
		o.DatabaseName = strings.ReplaceAll(o.Name, "-", "_")
	}
	if o.DatabaseSchema == "" {
		o.DatabaseSchema = strings.ReplaceAll(o.Name, "-", "_")
	}
	if o.Ref == "" {
		o.Ref = "main"
	}
	if o.Description == "" {
		o.Description = "Production-ready Go microservice " + o.Name + "."
	}
	parts := strings.Split(o.Module, "/")
	if len(parts) == 3 && parts[0] == "github.com" {
		if o.Image == "" {
			o.Image = "ghcr.io/" + strings.ToLower(parts[1]+"/"+parts[2])
		}
		if o.BufModule == "" {
			o.BufModule = "buf.build/" + strings.ToLower(parts[1]+"/"+parts[2])
		}
	}
}

func (o Options) Validate() error {
	if !serviceNamePattern.MatchString(o.Name) || len(o.Name) > 63 {
		return errors.New("name must be a lowercase DNS label of at most 63 characters")
	}
	if !serviceNamePattern.MatchString(o.Namespace) || len(o.Namespace) > 63 {
		return errors.New("namespace must be a lowercase DNS label of at most 63 characters")
	}
	if !migrationTablePattern.MatchString(o.MigrationTable) {
		return errors.New("migration-table must contain lowercase letters, digits, or underscores and be at most 63 characters")
	}
	if !migrationTablePattern.MatchString(o.DatabaseName) {
		return errors.New("database-name must contain lowercase letters, digits, or underscores and be at most 63 characters")
	}
	if !migrationTablePattern.MatchString(o.DatabaseSchema) {
		return errors.New("database-schema must contain lowercase letters, digits, or underscores and be at most 63 characters")
	}
	if err := module.CheckPath(o.Module); err != nil {
		return fmt.Errorf("invalid Go module path: %w", err)
	}
	if o.Output == "" {
		return errors.New("output is required")
	}
	if o.Image == "" {
		return errors.New("image is required; set --image for non-GitHub modules")
	}
	if !strings.HasPrefix(o.BufModule, "buf.build/") || len(strings.Split(o.BufModule, "/")) != 3 {
		return errors.New("buf-module must have the form buf.build/owner/module")
	}
	if o.Ref == "" {
		return errors.New("ref is required")
	}
	return nil
}

func Generate(ctx context.Context, options Options) (string, error) {
	options.Defaults()
	if err := options.Validate(); err != nil {
		return "", err
	}
	output, err := filepath.Abs(options.Output)
	if err != nil {
		return "", fmt.Errorf("resolve output: %w", err)
	}
	if _, err := os.Stat(output); err == nil {
		return "", fmt.Errorf("output already exists: %s", output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect output: %w", err)
	}
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("create output parent: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".microgen-*")
	if err != nil {
		return "", fmt.Errorf("create temporary output: %w", err)
	}
	defer os.RemoveAll(temporary)

	if options.Source != "" {
		err = copyLocal(options.Source, temporary)
	} else {
		err = downloadTemplate(ctx, options.Ref, temporary)
	}
	if err != nil {
		return "", err
	}
	replacements := []struct{ old, new string }{
		{templateModule, options.Module},
		{templateImage, options.Image},
		{templateBufModule, options.BufModule},
		{templateNamespace, options.Namespace},
		{templateMigrationTable, options.MigrationTable},
		{templateDatabaseName, options.DatabaseName},
		{templateDatabaseSchema, options.DatabaseSchema},
		{templateDescription, options.Description},
		{templateName, options.Name},
	}
	if err := rewriteProject(temporary, options.Module, replacements); err != nil {
		return "", err
	}
	if options.GenerateCode {
		command := exec.CommandContext(ctx, "buf", "generate")
		command.Dir = temporary
		if data, commandErr := command.CombinedOutput(); commandErr != nil {
			return "", fmt.Errorf("generate protobuf code with Buf: %w: %s", commandErr, strings.TrimSpace(string(data)))
		}
	}
	if options.Tidy {
		command := exec.CommandContext(ctx, "go", "mod", "tidy")
		command.Dir = temporary
		if data, commandErr := command.CombinedOutput(); commandErr != nil {
			return "", fmt.Errorf("tidy generated Go module: %w: %s", commandErr, strings.TrimSpace(string(data)))
		}
	}
	if options.InitGit {
		command := exec.CommandContext(ctx, "git", "init", "-b", "main")
		command.Dir = temporary
		if data, commandErr := command.CombinedOutput(); commandErr != nil {
			return "", fmt.Errorf("initialize Git repository: %w: %s", commandErr, strings.TrimSpace(string(data)))
		}
	}
	if err := os.Rename(temporary, output); err != nil {
		return "", fmt.Errorf("publish generated project: %w", err)
	}
	return output, nil
}

func copyLocal(source, destination string) error {
	root, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("resolve source: %w", err)
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if skipped(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("template contains unsupported symlink: %s", relative)
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target, entry)
	})
}

func downloadTemplate(ctx context.Context, ref, destination string) error {
	target := "https://api.github.com/repos/lihongjie0209/go-api-template/tarball/" + url.PathEscape(ref)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("create template request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "microgen")
	client := &http.Client{Timeout: 2 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download template: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download template: unexpected HTTP status %s", response.Status)
	}
	limited := &io.LimitedReader{R: response.Body, N: maxArchiveBytes + 1}
	if err := extractTarGzip(limited, destination); err != nil {
		return err
	}
	if limited.N <= 0 {
		return errors.New("template archive exceeds 100 MiB")
	}
	return nil
}

func extractTarGzip(source io.Reader, destination string) error {
	gzipReader, err := gzip.NewReader(source)
	if err != nil {
		return fmt.Errorf("open template archive: %w", err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read template archive: %w", err)
		}
		parts := strings.Split(strings.TrimPrefix(filepath.ToSlash(header.Name), "/"), "/")
		if len(parts) < 2 {
			continue
		}
		relative := filepath.FromSlash(strings.Join(parts[1:], "/"))
		if skipped(relative) {
			continue
		}
		target := filepath.Join(destination, relative)
		if !within(destination, target) {
			return fmt.Errorf("template archive contains invalid path %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(header.Mode)&0o755)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return errors.Join(copyErr, closeErr)
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("template archive contains unsupported link %q", header.Name)
		}
	}
}

func rewriteProject(root, modulePath string, replacements []struct{ old, new string }) error {
	goModPath := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return fmt.Errorf("read go.mod: %w", err)
	}
	parsed, err := modfile.Parse(goModPath, data, nil)
	if err != nil {
		return fmt.Errorf("parse go.mod: %w", err)
	}
	if err := parsed.AddModuleStmt(modulePath); err != nil {
		return fmt.Errorf("set Go module: %w", err)
	}
	formatted, err := parsed.Format()
	if err != nil {
		return fmt.Errorf("format go.mod: %w", err)
	}
	if err := os.WriteFile(goModPath, formatted, 0o644); err != nil {
		return fmt.Errorf("write go.mod: %w", err)
	}
	goReplacements := replacements
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if path == goModPath {
			return nil
		}
		if filepath.Ext(path) == ".go" {
			return rewriteGoFile(path, goReplacements)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.IndexByte(string(data), 0) >= 0 {
			return nil
		}
		updated := string(data)
		updated = stripTemplateOnly(updated)
		for _, replacement := range replacements {
			updated = strings.ReplaceAll(updated, replacement.old, replacement.new)
		}
		if updated == string(data) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(path, []byte(updated), info.Mode().Perm())
	})
}

func stripTemplateOnly(value string) string {
	for _, markers := range [][2]string{
		{"<!-- microgen:template-only:start -->", "<!-- microgen:template-only:end -->"},
		{"# microgen:template-only:start", "# microgen:template-only:end"},
	} {
		for {
			start := strings.Index(value, markers[0])
			if start < 0 {
				break
			}
			endRelative := strings.Index(value[start:], markers[1])
			if endRelative < 0 {
				break
			}
			end := start + endRelative + len(markers[1])
			if end < len(value) && value[end] == '\n' {
				end++
			}
			value = value[:start] + value[end:]
		}
	}
	return value
}

func rewriteGoFile(path string, replacements []struct{ old, new string }) error {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse Go file %s: %w", path, err)
	}
	// Generated protobuf descriptors contain length-delimited binary data.
	// Buf regenerates these files after the source .proto is rewritten.
	if ast.IsGenerated(file) {
		return nil
	}
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			return true
		}
		updated := applyReplacements(value, replacements)
		if updated != value {
			literal.Value = strconv.Quote(updated)
		}
		return true
	})
	for _, group := range file.Comments {
		for _, comment := range group.List {
			comment.Text = applyReplacements(comment.Text, replacements)
		}
	}
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	formatErr := format.Node(output, fileSet, file)
	closeErr := output.Close()
	if formatErr != nil || closeErr != nil {
		return errors.Join(formatErr, closeErr)
	}
	return nil
}

func applyReplacements(value string, replacements []struct{ old, new string }) string {
	for _, replacement := range replacements {
		value = strings.ReplaceAll(value, replacement.old, replacement.new)
	}
	return value
}

func skipped(relative string) bool {
	clean := filepath.ToSlash(filepath.Clean(relative))
	first := strings.Split(clean, "/")[0]
	if first == ".git" || first == "bin" || first == "logs" || first == "coverage.out" || strings.HasPrefix(first, ".microgen-") {
		return true
	}
	return clean == "cmd/microgen" || strings.HasPrefix(clean, "cmd/microgen/") ||
		clean == "internal/scaffold" || strings.HasPrefix(clean, "internal/scaffold/") ||
		clean == "internal/microgencli" || strings.HasPrefix(clean, "internal/microgencli/")
}

func copyFile(source, destination string, entry fs.DirEntry) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	info, err := entry.Info()
	if err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}

func within(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
