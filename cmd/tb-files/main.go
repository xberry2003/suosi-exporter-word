package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"thoughtsexport/internal/tbinventory"
	"thoughtsexport/internal/tbweb"
	"thoughtsexport/internal/teambition/fileadapters"
	"thoughtsexport/internal/teambition/filecollector"
	"time"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tb-files <discover|download>")
		return 1
	}
	fs := flag.NewFlagSet("tb-files "+args[0], flag.ContinueOnError)
	projectURL := fs.String("project-url", "", "project file-library URL")
	output := fs.String("output", "./exports", "output root")
	resume := fs.Bool("resume", false, "resume confirmed discovery state")
	includeRaw := fs.Bool("include-raw", false, "write redacted source responses")
	downloadAssets := fs.Bool("download-assets", false, "download file bodies after discovery")
	maxFileSize := fs.Int64("max-file-size", 0, "maximum file size in bytes")
	concurrency := fs.Int("concurrency", 1, "bounded concurrency")
	since := fs.String("since", "", "optional RFC3339 lower bound")
	retryFailed := fs.Bool("retry-failed-downloads", false, "retry failed downloads")
	source := fs.String("source", "browser", "source adapter: browser, sdk, or offline")
	pageSize := fs.Int("page-size", 100, "source page size")
	profile := fs.String("profile-dir", "", "persistent browser profile directory")
	externalID := fs.String("external-id", "", "download only one stable source external_id")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if args[0] != "discover" && args[0] != "download" {
		fmt.Fprintln(os.Stderr, "unknown mode:", args[0])
		return 1
	}
	if *concurrency < 1 {
		fmt.Fprintln(os.Stderr, "--concurrency must be at least 1")
		return 1
	}
	ref, err := tbinventory.ParseProjectFilesURL(*projectURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --project-url:", err)
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cfg := filecollector.Config{ProjectID: ref.ProjectID, ProjectURL: *projectURL, Output: *output, Resume: *resume, IncludeRaw: *includeRaw, PageSize: *pageSize, Since: *since, MaxFileSize: *maxFileSize, Concurrency: *concurrency, RetryFailedDownloads: *retryFailed, DownloadExternalID: strings.TrimSpace(*externalID)}
	type fileSource interface {
		filecollector.PageSource
		filecollector.DownloadSource
	}
	var src fileSource
	var downloadHTTP = http.DefaultClient
	var browser *tbweb.BrowserSession
	if strings.EqualFold(*source, "offline") {
		if args[0] != "download" {
			fmt.Fprintln(os.Stderr, "--source offline is only valid for download upgrades of an existing package")
			return 1
		}
		src = nil
	} else if strings.EqualFold(*source, "sdk") {
		sdkCfg := tbinventory.LoadConfig()
		if err := sdkCfg.Validate(); err != nil {
			fmt.Fprintln(os.Stderr, "sdk authentication:", err)
			return 1
		}
		src = fileadapters.SDK{Client: tbinventory.NewSDKClient(sdkCfg)}
	} else if strings.EqualFold(*source, "browser") {
		profileDir := *profile
		if profileDir == "" {
			profileDir = filepath.Join(*output, "browser-profile")
		}
		var session tbweb.Session
		browser, session, err = openBrowser(ctx, *projectURL, profileDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "browser session:", err)
			return 1
		}
		defer browser.Close()
		client := tbweb.NewClient(session.CookieHeader, session.Referer)
		// Listing calls are short, but large signed assets may take longer than
		// the browser client's default request timeout.
		client.HTTP.Timeout = 15 * time.Minute
		src = fileadapters.Browser{Client: client}
		downloadHTTP = client.HTTP
	} else {
		fmt.Fprintln(os.Stderr, "--source must be browser, sdk, or offline")
		return 1
	}
	partial := false
	if args[0] == "discover" {
		result, discoverErr := filecollector.Discover(ctx, src, cfg)
		if discoverErr != nil {
			fmt.Fprintf(os.Stderr, "discover failed: %v\n", discoverErr)
			return 1
		}
		fmt.Printf("discover complete: project=%s nodes=%d directories=%d files=%d pages=%d errors=%d unresolved_parents=%d output=%s\n", ref.ProjectID, result.Nodes, result.Directories, result.Files, result.Pages, result.Errors, result.UnresolvedParents, filepath.Join(*output, "teambition-file-collector", ref.ProjectID))
		partial = result.Errors > 0 || result.UnresolvedParents > 0
		if !*downloadAssets {
			if partial {
				return 2
			}
			return 0
		}
	}
	downloadResult, downloadErr := filecollector.Download(ctx, src, downloadHTTP, cfg)
	if downloadErr != nil {
		fmt.Fprintf(os.Stderr, "download failed: %v\n", downloadErr)
		return 1
	}
	fmt.Printf("download complete: project=%s downloaded=%d skipped=%d failed=%d permission_denied=%d too_large=%d bytes=%d output=%s\n", ref.ProjectID, downloadResult.Downloaded, downloadResult.Skipped, downloadResult.Failed, downloadResult.PermissionDenied, downloadResult.TooLarge, downloadResult.Bytes, filepath.Join(*output, "teambition-file-collector", ref.ProjectID))
	if partial || downloadResult.Failed > 0 {
		return 2
	}
	return 0
}

func openBrowser(ctx context.Context, url, profile string) (*tbweb.BrowserSession, tbweb.Session, error) {
	return tbweb.OpenBrowserSession(ctx, url, profile, 10*time.Minute)
}
