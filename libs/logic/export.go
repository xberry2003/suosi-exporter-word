package logic

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"thoughtsexport/libs/utils"
)

type ExportOptions struct {
	URL              string
	OutputRoot       string
	Format           string
	IncludeTemplates bool
	Overwrite        bool
	RetryFailed      bool
	DryRun           bool
	MockData         string
}

func ExportWorkspace(opts ExportOptions) error {
	if opts.OutputRoot == "" {
		opts.OutputRoot = "exports"
	}
	if opts.Format == "" {
		opts.Format = "docx"
	}
	if err := os.MkdirAll(opts.OutputRoot, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll("logs", 0755); err != nil {
		return err
	}
	logFile := filepath.Join("logs", "export_"+time.Now().Format("20060102_150405")+".log")
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	logger := log.New(f, "", log.LstdFlags)
	logger.Printf("start export url=%s output=%s format=%s dryRun=%v mock=%s", opts.URL, opts.OutputRoot, opts.Format, opts.DryRun, opts.MockData)

	if opts.MockData != "" {
		return runMockExport(opts, logger)
	}
	if opts.URL == "" {
		return errors.New("url is required")
	}
	parts := strings.Split(opts.URL, "/")
	if len(parts) < 5 {
		return fmt.Errorf("URL 格式不正确: %s", opts.URL)
	}
	hashSpace := parts[4]
	req := NewRequest("", hashSpace)
	cookie := GetLoginCookieString(opts.URL, "TB_ACCESS_TOKEN")
	req.cookie = cookie

	workspace, err := req.GetWorkspace(hashSpace)
	if err != nil {
		return err
	}
	needCloseOutput := false
	if workspace.WorkspaceSecurity.DisableOutput {
		succeed, err := req.EnableOutput(hashSpace, true)
		if err != nil {
			return err
		}
		if !succeed {
			return errors.New("本文档无法下载，开启导出权限失败。请文档所有者在本工具登录后再尝试")
		}
		needCloseOutput = true
	}
	defer func() {
		if needCloseOutput {
			_, _ = req.EnableOutput(hashSpace, false)
		}
	}()

	orgName := sanitizeName(workspace.Organization.Name)
	spaceName := sanitizeName(workspace.Name)
	workspaceRoot := filepath.Join(opts.OutputRoot, orgName, spaceName)
	if err := os.MkdirAll(workspaceRoot, 0755); err != nil {
		return err
	}
	manifestPath := filepath.Join(workspaceRoot, "manifest.json")
	store := NewManifestStore(manifestPath)
	_ = store.Load()

	nodes, err := req.GetAllNodes(hashSpace, "")
	if err != nil {
		return err
	}
	tree := buildNodeTree(nodes, hashSpace)
	logger.Printf("workspace ready: %s/%s nodes=%d", orgName, spaceName, len(nodes))

	if opts.DryRun {
		logger.Println("dry-run enabled, only rendering tree")
		return renderTreeToFile(filepath.Join(workspaceRoot, "dry_run_tree.txt"), tree)
	}
	if err := exportChildren(req, opts, logger, store, workspaceRoot, "", tree.Roots); err != nil {
		return err
	}
	if !opts.IncludeTemplates {
		return nil
	}
	templates, err := req.GetTemplates(hashSpace)
	if err != nil {
		return err
	}
	result, err := exportTemplatesForWorkspace(req, TemplateExportOptions{
		URL:         opts.URL,
		OutputRoot:  opts.OutputRoot,
		Cookie:      cookie,
		Overwrite:   opts.Overwrite,
		RetryFailed: opts.RetryFailed,
	}, filepath.Join(workspaceRoot, "templates"), templates)
	if err != nil {
		return err
	}
	logTemplateResult(logger, result)
	return nil
}

type treeNode struct {
	Node     *Node
	Children []*treeNode
}

type nodeTree struct {
	Roots []*treeNode
}

func buildNodeTree(nodes []*Node, workspaceHash string) nodeTree {
	nodeByID := map[string]*treeNode{}
	childrenByParent := map[string][]*treeNode{}
	for _, n := range nodes {
		nodeByID[n.ID] = &treeNode{Node: n}
	}
	for _, n := range nodes {
		parent := n.ParentId
		if parent == "" || parent == workspaceHash {
			continue
		}
		childrenByParent[parent] = append(childrenByParent[parent], nodeByID[n.ID])
	}
	roots := []*treeNode{}
	for _, n := range nodes {
		if n.ParentId == "" || n.ParentId == workspaceHash {
			roots = append(roots, nodeByID[n.ID])
		}
	}
	for id, tn := range nodeByID {
		tn.Children = childrenByParent[id]
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Node.Title < roots[j].Node.Title })
	return nodeTree{Roots: roots}
}

func exportChildren(req *Request, opts ExportOptions, logger *log.Logger, store *ManifestStore, parentDir string, parentTitle string, children []*treeNode) error {
	sort.Slice(children, func(i, j int) bool { return children[i].Node.Title < children[j].Node.Title })
	for _, child := range children {
		if err := exportSingle(req, opts, logger, store, parentDir, parentTitle, child); err != nil {
			logger.Printf("failed: %s %v", child.Node.Title, err)
		}
	}
	return nil
}

func exportSingle(req *Request, opts ExportOptions, logger *log.Logger, store *ManifestStore, parentDir string, parentTitle string, current *treeNode) error {
	node := current.Node
	entry := ManifestEntry{
		Title:      node.Title,
		NodeID:     node.ID,
		URL:        node.ID,
		Parent:     parentTitle,
		Status:     "pending",
		ExportTime: time.Now().Format(time.RFC3339),
	}

	if old, ok := store.Get(node.ID); ok {
		if old.LocalPath != "" {
			entry.LocalPath = old.LocalPath
		}
		if old.Parent != "" {
			entry.Parent = old.Parent
		}
		if old.Title != "" {
			entry.Title = old.Title
		}
	}

	if entry.LocalPath == "" {
		baseName := sanitizeName(node.Title)
		if len(current.Children) > 0 {
			entry.LocalPath = filepath.Join(parentDir, baseName, baseName+".docx")
		} else {
			entry.LocalPath = filepath.Join(parentDir, baseName+".docx")
		}
	}

	if len(current.Children) > 0 {
		dirPath := filepath.Dir(entry.LocalPath)
		if utils.FileExist(entry.LocalPath) && !opts.Overwrite {
			entry.Status = "skipped"
			store.Upsert(entry)
			_ = store.Save()
			return exportChildren(req, opts, logger, store, dirPath, node.Title, current.Children)
		}
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			entry.Status = "failed"
			entry.Reason = err.Error()
			store.Upsert(entry)
			_ = store.Save()
			return err
		}
		if err := exportNodeDoc(req, opts, node, entry.LocalPath); err != nil {
			entry.Status = "failed"
			entry.Reason = err.Error()
			store.Upsert(entry)
			_ = store.Save()
		} else {
			entry.Status = "success"
			store.Upsert(entry)
			_ = store.Save()
		}
		return exportChildren(req, opts, logger, store, dirPath, node.Title, current.Children)
	}

	if utils.FileExist(entry.LocalPath) && !opts.Overwrite {
		entry.Status = "skipped"
		store.Upsert(entry)
		_ = store.Save()
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(entry.LocalPath), 0755); err != nil {
		entry.Status = "failed"
		entry.Reason = err.Error()
		store.Upsert(entry)
		_ = store.Save()
		return err
	}
	if err := exportNodeDoc(req, opts, node, entry.LocalPath); err != nil {
		entry.Status = "failed"
		entry.Reason = err.Error()
		store.Upsert(entry)
		_ = store.Save()
		return err
	}
	entry.Status = "success"
	store.Upsert(entry)
	return store.Save()
}

func exportNodeDoc(req *Request, opts ExportOptions, node *Node, target string) error {
	if utils.FileExist(target) && !opts.Overwrite {
		return nil
	}
	if opts.DryRun {
		return nil
	}
	var downloadInfo *NodeDownload
	var err error
	if opts.Format == "html" {
		downloadInfo, err = req.GetDownloadUrl(node.ID, filepath.Base(target), "html")
	} else {
		downloadInfo, err = req.GetDownloadUrl(node.ID, filepath.Base(target), "docx")
	}
	if err != nil {
		downloadInfo, err = req.GetDownloadUrlByDetail(node.ID, filepath.Base(target))
		if err != nil {
			return err
		}
	}
	return DownloadFile(downloadInfo.DownURL, target, opts.Overwrite)
}

func DownloadFile(url string, filePath string, overwrite bool) error {
	if utils.FileExist(filePath) && !overwrite {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(file, resp.Body)
	return err
}

func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "untitled"
	}
	replacer := strings.NewReplacer(`\`, "_", `/`, "_", `:`, "_", `*`, "_", `?`, "_", `"`, "_", `<`, "_", `>`, "_", `|`, "_")
	name = replacer.Replace(name)
	name = strings.Trim(name, ". ")
	if name == "" {
		return "untitled"
	}
	return name
}

func renderTreeToFile(path string, tree nodeTree) error {
	var lines []string
	var walk func(prefix string, n *treeNode)
	walk = func(prefix string, n *treeNode) {
		lines = append(lines, prefix+n.Node.Title)
		for _, child := range n.Children {
			walk(prefix+"  ", child)
		}
	}
	for _, root := range tree.Roots {
		walk("", root)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
}

func runMockExport(opts ExportOptions, logger *log.Logger) error {
	mockPath := opts.MockData
	if mockPath == "" {
		mockPath = filepath.Join("mock", "mock_directory_tree.json")
	}
	data, err := os.ReadFile(mockPath)
	if err != nil {
		return err
	}
	var tree []MockNode
	if err := json.Unmarshal(data, &tree); err != nil {
		return err
	}
	root := filepath.Join(opts.OutputRoot, "企业名称", "知识库名称")
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	return createMockTree(root, tree, logger)
}

type MockNode struct {
	Title    string     `json:"title"`
	HasChild bool       `json:"has_child"`
	Children []MockNode `json:"children"`
}

func createMockTree(parentDir string, nodes []MockNode, logger *log.Logger) error {
	for _, n := range nodes {
		name := sanitizeName(n.Title)
		if len(n.Children) > 0 || n.HasChild {
			dir := filepath.Join(parentDir, name)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
			doc := filepath.Join(dir, name+".docx")
			if err := os.WriteFile(doc, []byte("mock"), 0644); err != nil {
				return err
			}
			if err := createMockTree(dir, n.Children, logger); err != nil {
				return err
			}
			continue
		}
		doc := filepath.Join(parentDir, name+".docx")
		if err := os.WriteFile(doc, []byte("mock"), 0644); err != nil {
			return err
		}
	}
	return nil
}
