package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"thoughtsexport/internal/tbinventory"
)

func TestLoadProjectsValidatesAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	data := []byte(`{"projects":[
		{"projectId":"p1","projectName":"one","projectUrl":"https://www.teambition.com/project/p1/works/r1","rootParentId":"r1"},
		{"projectId":"p1","projectName":"duplicate","projectUrl":"https://www.teambition.com/project/p1/works/r1","rootParentId":"r1"}
	]}`)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	projects, err := loadProjects(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].ID != "p1" || projects[0].RootParentID != "r1" {
		t.Fatalf("unexpected projects: %+v", projects)
	}
}

func TestLoadProjectsAcceptsUTF8BOM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"projects":[{"projectId":"p1","projectUrl":"https://www.teambition.com/project/p1/works/r1","rootParentId":"r1"}]}`)...)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	projects, err := loadProjects(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].ID != "p1" {
		t.Fatalf("unexpected projects: %+v", projects)
	}
}

func TestLoadProjectsRejectsMismatchedProjectID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	data := []byte(`{"projects":[{"projectId":"wrong","projectUrl":"https://www.teambition.com/project/p1/works/r1"}]}`)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProjects(path); err == nil {
		t.Fatal("expected mismatched project ID to fail")
	}
}

func TestExportCheckpointPreservesProjectURLList(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "tb_discovered_projects.json")
	output := filepath.Join(dir, "output")
	original := []byte(`{"generatedAt":"original","projects":[{"projectId":"p1","projectName":"one","projectUrl":"https://www.teambition.com/project/p1/works/r1","rootParentId":"r1"}]}`)
	if err := os.WriteFile(input, original, 0644); err != nil {
		t.Fatal(err)
	}
	db, err := tbinventory.OpenDB(filepath.Join(output, "tb_inventory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.UpsertProject(tbinventory.Project{ID: "p1", Name: "one", URL: "https://www.teambition.com/project/p1/works/r1", RootParentID: "r1"}, "success", ""); err != nil {
		t.Fatal(err)
	}
	if err := exportCheckpoint(db, output, input); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		GeneratedAt string          `json:"generatedAt"`
		Projects    []projectRecord `json:"projects"`
		Crawl       json.RawMessage `json:"crawl"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if root.GeneratedAt != "original" || len(root.Projects) != 1 || root.Projects[0].RootParent != "r1" {
		t.Fatalf("original discovery data was not preserved: %+v", root)
	}
	if len(root.Crawl) == 0 || !json.Valid(root.Crawl) {
		t.Fatal("crawl artifact was not embedded")
	}
	summaryPath := filepath.Join(dir, "tb_summary.json")
	summary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("standalone summary was not written: %v", err)
	}
	if !json.Valid(summary) {
		t.Fatal("standalone summary is invalid JSON")
	}
}
