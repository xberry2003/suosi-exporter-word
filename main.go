package main

import (
	"encoding/json"
	"flag"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"thoughtsexport/libs/logic"
)

const defaultListenAddr = "127.0.0.1:43821"

var (
	runMu   sync.Mutex
	running bool
)

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
	flag.Parse()
	return cfg
}

func startWebMode(cfg cliConfig) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		const tpl = `<!doctype html><html><head><meta charset="utf-8"><title>Thoughts Export</title>
<style>body{font-family:Arial,sans-serif;margin:32px auto;max-width:900px}input,select{width:100%;padding:8px;margin:6px 0 16px}button{padding:10px 18px}</style>
</head><body><h1>Thoughts Export</h1>
<form method="POST" action="/receive/url">
<label>Workspace URL</label><input name="url" placeholder="https://thoughts.teambition.com/workspaces/.../overview" />
<label>Output root</label><input name="output" value="exports" />
<label>Format</label><select name="format"><option value="docx">docx</option><option value="html">html</option></select>
<label><input type="checkbox" name="include_templates" /> export knowledge-base templates (DOCX + HTML)</label><br>
<label><input type="checkbox" name="overwrite" /> overwrite</label><br>
<label><input type="checkbox" name="retry_failed" /> retry failed</label><br>
<button type="submit">Start</button>
</form></body></html>`
		_ = template.Must(template.New("web").Parse(tpl)).Execute(w, nil)
	})
	mux.HandleFunc("/receive/url", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		next := cliConfig{
			URL:              r.FormValue("url"),
			Output:           firstNonEmpty(r.FormValue("output"), cfg.Output, "exports"),
			Format:           firstNonEmpty(r.FormValue("format"), cfg.Format, "docx"),
			IncludeTemplates: r.FormValue("include_templates") == "on",
			Overwrite:        r.FormValue("overwrite") == "on",
			RetryFailed:      r.FormValue("retry_failed") == "on",
			DryRun:           cfg.DryRun,
			MockData:         cfg.MockData,
		}
		if next.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		runMu.Lock()
		if running {
			runMu.Unlock()
			writeJSON(w, map[string]interface{}{"success": true, "message": "task already running"})
			return
		}
		running = true
		runMu.Unlock()
		go func() {
			defer func() {
				runMu.Lock()
				running = false
				runMu.Unlock()
			}()
			if err := execute(next); err != nil {
				log.Println(err)
			}
			os.Exit(0)
		}()
		writeJSON(w, map[string]interface{}{"success": true, "message": "task started"})
	})
	srv := &http.Server{Addr: defaultListenAddr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Println(err)
		}
	}()
	_ = logic.OpenURL("http://" + defaultListenAddr + "/")
	select {}
}

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

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
