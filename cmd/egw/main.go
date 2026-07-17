package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/external_gateway/internal/audit"
	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
	"github.com/external_gateway/internal/secrets"
	"github.com/external_gateway/internal/server"
	"github.com/kardianos/service"
	"github.com/rs/zerolog"
)

var (
	configPath = flag.String("config", "", "Path to config file")
	opsPath    = flag.String("ops", "", "Path to operations config file")
	port       = flag.Int("port", 8443, "Port to listen on")
	verbose    = flag.Bool("v", false, "Verbose logging")
	svcInstall = flag.Bool("install", false, "Install as Windows service")
	svcRemove  = flag.Bool("remove", false, "Remove Windows service")
	svcName    = flag.String("svc-name", "external_gateway", "Windows service name")
)

type program struct {
	httpSrv *http.Server
	ln      net.Listener
	logger  zerolog.Logger
}

func (p *program) Start(s service.Service) error {
	go p.run()
	return nil
}

func (p *program) run() {
	p.logger.Info().Str("addr", p.ln.Addr().String()).Msg("starting gateway")
	if err := p.httpSrv.Serve(p.ln); err != nil && err != http.ErrServerClosed {
		p.logger.Fatal().Err(err).Msg("server error")
	}
}

func (p *program) Stop(s service.Service) error {
	p.logger.Info().Msg("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.httpSrv.Shutdown(ctx)
}

func main() {
	flag.Parse()

	logger := zerolog.New(os.Stderr).With().Timestamp().Caller().Logger()
	if *verbose {
		logger = logger.Level(zerolog.DebugLevel)
	}

	// Build service argument list for SCM registration
	// Includes config/ops/port so the service starts with the right settings
	var svcArgs []string
	if *configPath != "" {
		svcArgs = append(svcArgs, "--config", *configPath)
	}
	if *opsPath != "" {
		svcArgs = append(svcArgs, "--ops", *opsPath)
	}
	if flagWasSet("port") {
		svcArgs = append(svcArgs, "--port", strconv.Itoa(*port))
	}
	if *svcName != "external_gateway" {
		svcArgs = append(svcArgs, "--svc-name", *svcName)
	}
	svcCfg := &service.Config{
		Name:        *svcName,
		DisplayName: fmt.Sprintf("External Gateway (%s)", *svcName),
		Description: "Hardened proxy for LLM agents to call external services",
		Arguments:   svcArgs,
	}

	// Install/remove don't need config
	if *svcInstall || *svcRemove {
		p := &program{logger: logger}
		svc, err := service.New(p, svcCfg)
		if err != nil {
			log.Fatal(err)
		}
		if *svcInstall {
			if err := svc.Install(); err != nil {
				log.Fatal(err)
			}
			logger.Info().Msg("service installed — run as Administrator if this failed")
			return
		}
		if *svcRemove {
			if err := svc.Uninstall(); err != nil {
				log.Fatal(err)
			}
			logger.Info().Msg("service removed")
			return
		}
	}

	cfg, opsPath_, port_ := resolveConfig(logger)
	_, ln, httpSrv := setup(cfg, opsPath_, port_, logger)

	p := &program{httpSrv: httpSrv, ln: ln, logger: logger}
	svc, err := service.New(p, svcCfg)
	if err != nil {
		log.Fatal(err)
	}
	if err := svc.Run(); err != nil {
		log.Fatal(err)
	}
}

func resolveConfig(logger zerolog.Logger) (*config.Config, string, int) {
	if *configPath == "" {
		dir, _ := os.Getwd()
		*configPath = filepath.Join(dir, "config.yaml")
		if _, err := os.Stat(*configPath); os.IsNotExist(err) {
			*configPath = filepath.Join(dir, "config.example", "config.yaml")
		}
	}

	if *opsPath == "" {
		dir, _ := os.Getwd()
		*opsPath = filepath.Join(dir, "operations.yaml")
		if _, err := os.Stat(*opsPath); os.IsNotExist(err) {
			*opsPath = filepath.Join(dir, "config.example", "operations.yaml")
		}
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Fatal().Err(err).Msg("loading config")
	}
	// Port precedence: explicit --port flag > config server.port > default (8443).
	// Makes the port derivable from config.<x>.yaml; deploy tooling still passes
	// --port so the installed service records an explicit port.
	effectivePort := *port
	if !flagWasSet("port") && cfg.Server.Port > 0 {
		effectivePort = cfg.Server.Port
	}
	return cfg, *opsPath, effectivePort
}

// flagWasSet reports whether the named flag was explicitly provided on the CLI.
func flagWasSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func setup(cfg *config.Config, opsPath_ string, port_ int, logger zerolog.Logger) (*server.Server, net.Listener, *http.Server) {
	if err := audit.Init(cfg.Audit.Dir, cfg.Audit.MaxSizeMB); err != nil {
		logger.Fatal().Err(err).Msg("initializing audit")
	}

	secrets.GetResolver()
	switch cfg.SecretStore.Backend {
	case "pass":
		secrets.SetStore(&secrets.PassStore{Dir: cfg.SecretStore.PassDir})
	case "keepass":
		secrets.SetStore(&secrets.KeePassStore{
			Path:     cfg.SecretStore.KDBXPath,
			Password: cfg.SecretStore.KDBXSecret,
		})
	}
	secrets.GetTaintRegistry()

	logger.Info().Str("ops", opsPath_).Msg("loading operations")
	reg, err := policy.NewRegistry(opsPath_)
	if err != nil {
		logger.Fatal().Err(err).Msg("creating operation registry")
	}
	reg.TunnelChecker = newTunnelChecker(cfg)

	srv := server.New(cfg, reg, opsPath_, logger)

	listenAddr := fmt.Sprintf("127.0.0.1:%d", port_)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		logger.Fatal().Err(err).Msg("listening")
	}

	httpSrv := &http.Server{
		Addr:         listenAddr,
		Handler:      srv,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	return srv, ln, httpSrv
}
