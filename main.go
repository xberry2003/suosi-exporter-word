package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"thoughtsexport/internal/control"
	"thoughtsexport/libs/logic"
)

const defaultListenAddr = "127.0.0.1:43821"

type cliConfig struct {
	URL              string
	Output           string
	Format           string
	IncludeTemplates bool
	Overwrite        bool
	RetryFailed      bool
	DryRun           bool
	MockData         string
	Serve            bool
	Listen           string
	DataDir          string
	WebOutput        string
	WebConcurrency   int
	OpenBrowser      bool
}

func main() {
	cfg := parseFlags()
	if cfg.Serve || cfg.URL == "" {
		startWebMode(cfg)
		return
	}
	if err := execute(cfg); err != nil {
		log.Fatal(err)
	}
}

func parseFlags() cliConfig {
	cfg := cliConfig{}
	flag.StringVar(&cfg.URL, "url", "", "workspace URL")
	flag.StringVar(&cfg.Output, "output", filepath.Join("exports"), "output root")
	flag.StringVar(&cfg.Format, "format", "docx", "export format")
	flag.BoolVar(&cfg.IncludeTemplates, "include-templates", false, "export knowledge-base templates as DOCX and HTML packages")
	flag.BoolVar(&cfg.Overwrite, "overwrite", false, "overwrite existing files")
	flag.BoolVar(&cfg.RetryFailed, "retry-failed", false, "retry failed items")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "dry run only")
	flag.StringVar(&cfg.MockData, "mock-data", "", "mock tree json file")
	flag.BoolVar(&cfg.Serve, "serve", false, "force web mode")
	flag.StringVar(&cfg.Listen, "listen", defaultListenAddr, "web listen address")
	flag.StringVar(&cfg.DataDir, "data-dir", filepath.Join("runtime", "data"), "web runtime data directory")
	flag.StringVar(&cfg.WebOutput, "web-output", filepath.Join("runtime", "artifacts"), "web job artifact directory")
	flag.IntVar(&cfg.WebConcurrency, "web-concurrency", 1, "maximum concurrent web jobs")
	flag.BoolVar(&cfg.OpenBrowser, "open-browser", true, "open the web console in the default browser")
	flag.Parse()
	return cfg
}

func startWebMode(cfg cliConfig) {
	app, err := control.NewServer(control.ServerConfig{
		DatabasePath: filepath.Join(cfg.DataDir, "jobs.sqlite"),
		ArtifactRoot: cfg.WebOutput,
		DataRoot:     cfg.DataDir,
		Concurrency:  cfg.WebConcurrency,
		Auth: control.AuthConfig{
			APIBaseURL:    firstNonEmpty(os.Getenv("AUTH_API_BASE_URL"), "http://43.142.31.198:9999/emp/api"),
			SessionSecret: firstNonEmpty(os.Getenv("AUTH_SESSION_SECRET"), controlRandomSessionSecret()),
		},
		SecureCookie: strings.EqualFold(os.Getenv("AUTH_COOKIE_SECURE"), "true"),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()
	srv := &http.Server{Addr: cfg.Listen, Handler: app, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	address := "http://" + cfg.Listen + "/"
	log.Printf("采集控制台已启动: %s", address)
	if cfg.OpenBrowser {
		_ = logic.OpenURL(address)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func controlRandomSessionSecret() string { return control.RandomSessionSecret() }

func execute(cfg cliConfig) error {
	if cfg.Format == "" {
		cfg.Format = "docx"
	}
	cfg.Output = firstNonEmpty(cfg.Output, "exports")
	return logic.ExportWorkspace(logic.ExportOptions{
		URL:              cfg.URL,
		OutputRoot:       cfg.Output,
		Format:           cfg.Format,
		IncludeTemplates: cfg.IncludeTemplates,
		Overwrite:        cfg.Overwrite,
		RetryFailed:      cfg.RetryFailed,
		DryRun:           cfg.DryRun,
		MockData:         cfg.MockData,
	})
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
