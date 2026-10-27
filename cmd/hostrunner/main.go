package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/jerryctt/hostrunner-mcp/internal/config"
	"github.com/jerryctt/hostrunner-mcp/internal/exec"
	internalserver "github.com/jerryctt/hostrunner-mcp/internal/server"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	cfgFlag := flag.String("config", "", "path to config.yaml (default: ~/.config/hostrunner/config.yaml, or set HOSTRUNNER_CONFIG)")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	log := zerolog.New(os.Stderr).With().Timestamp().Logger()

	cfgPath := config.ResolvePath(*cfgFlag)
	if cfgPath == "" {
		log.Fatal().Msg("cannot determine config path; pass -config or set HOSTRUNNER_CONFIG")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", cfgPath).Msg("load config (create it from examples/config.example.yaml, or set HOSTRUNNER_CONFIG / -config)")
	}
	// Config is read once at startup: after editing it, restart Claude Desktop /
	// the Codex app (which respawns this server). This line makes the effective values
	// verifiable in mcp-server-hostrunner.log.
	log.Info().
		Str("config", cfgPath).
		Str("timeout", cfg.Timeout.String()).
		Strs("allowed_roots", cfg.AllowedRoots).
		Strs("allowed_commands", cfg.AllowedCommands).
		Str("codex_command", cfg.Agent("codex").Command).
		Str("claude_command", cfg.Agent("claude").Command).
		Msg("config loaded")

	s, shutdown := internalserver.New(cfg, exec.Runner{}, log)
	err = mcpserver.ServeStdio(s)
	// Cancel any in-flight background jobs so their CLI process groups are
	// killed rather than orphaned when this process exits.
	shutdown()
	if err != nil {
		log.Fatal().Err(err).Msg("server exited")
	}
}
