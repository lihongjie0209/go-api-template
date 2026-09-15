package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/lihongjie0209/go-api-template/internal/app"
	"github.com/lihongjie0209/go-api-template/internal/buildinfo"
	"github.com/lihongjie0209/go-api-template/internal/config"
)

// @title Go API Template
// @version 1.0
// @description Production-oriented Go Web API scaffold. Stable application codes: 0 success; 10001 invalid argument; 10004 not found; 10008 timeout; 10029 throttled; 20001 unauthorized; 20003 forbidden; 30009 conflict; 30010 processing; 50000 internal; 50003 dependency unavailable; 50004 authorization unavailable; 50005 route policy missing.
// @BasePath /
// @schemes http https
// @securityDefinitions.apikey Bearer
// @in header
// @name Authorization
// @description Enter "Bearer {JWT}".
// @securityDefinitions.apikey PSK
// @in header
// @name Authorization
// @description Enter "PSK {shared-key}" for routes configured with PSK authentication.
func main() {
	configPath := flag.String("config", "", "configuration file path (default: discover config.* in . or ./config)")
	profile := flag.String("env", "", "active environment profile (overrides APP_ENV and config)")
	showVersion := flag.Bool("version", false, "print build version information and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("version=%s commit=%s build_time=%s\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime)
		return
	}
	cfg, err := config.LoadWithProfile(*configPath, *profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load configuration: %v\n", err)
		os.Exit(1)
	}
	app.New(cfg).Run()
}
