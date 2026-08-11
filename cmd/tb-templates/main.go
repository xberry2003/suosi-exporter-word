package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"thoughtsexport/libs/logic"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("tb-templates", flag.ContinueOnError)
	workspaceURL := fs.String("url", "", "Thoughts workspace overview URL")
	output := fs.String("output", filepath.Join("exports"), "output root")
	templateID := fs.String("template-id", "", "export only one template ID")
	includeRaw := fs.Bool("include-raw", false, "deprecated; raw source and preview are always retained")
	overwrite := fs.Bool("overwrite", false, "overwrite existing template files")
	retryFailed := fs.Bool("retry-failed", false, "retry templates marked failed in the manifest")
	dryRun := fs.Bool("dry-run", false, "discover templates without downloading files")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	result, err := logic.ExportTemplates(logic.TemplateExportOptions{
		URL:         *workspaceURL,
		OutputRoot:  *output,
		TemplateID:  *templateID,
		IncludeRaw:  *includeRaw,
		Overwrite:   *overwrite,
		RetryFailed: *retryFailed,
		DryRun:      *dryRun,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "template export failed:", err)
		return 1
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(data))
	if result.Failed > 0 {
		return 2
	}
	return 0
}
