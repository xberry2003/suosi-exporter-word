package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"thoughtsexport/libs/logic"
)

func main() {
	homeURL := flag.String("url", "https://thoughts.teambition.com/", "Thoughts home or organization workspaces URL")
	output := flag.String("output", "exports/templates-all", "output root")
	profileDir := flag.String("profile-dir", "", "persistent Chrome profile directory; defaults to the user config directory")
	overwrite := flag.Bool("overwrite", false, "overwrite changed template files")
	retryFailed := flag.Bool("retry-failed", false, "retry templates previously marked failed")
	dryRun := flag.Bool("dry-run", false, "discover workspaces and templates without rendering")
	flag.Parse()
	result, err := logic.ExportAllTemplates(logic.TemplateBatchOptions{HomeURL: *homeURL, OutputRoot: *output, ProfileDir: *profileDir, Overwrite: *overwrite, RetryFailed: *retryFailed, DryRun: *dryRun})
	if err != nil {
		fmt.Fprintln(os.Stderr, "batch template export failed:", err)
		os.Exit(1)
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(data))
	if result.Failed > 0 {
		os.Exit(2)
	}
}
