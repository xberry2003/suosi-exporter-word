package control

import (
	"archive/zip"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed web/*
var webFiles embed.FS

type Server struct {
	store        *Store
	manager      *Manager
	auth         *AuthService
	secureCookie bool
	handler      http.Handler
}

type ServerConfig struct {
	DatabasePath string
	ArtifactRoot string
	DataRoot     string
	Concurrency  int
	Auth         AuthConfig
	SecureCookie bool
}

func NewServer(config ServerConfig) (*Server, error) {
	var auth *AuthService
	if strings.TrimSpace(config.Auth.APIBaseURL) != "" {
		var err error
		auth, err = NewAuthService(config.Auth)
		if err != nil {
			return nil, err
		}
	}
	store, err := OpenStore(config.DatabasePath)
	if err != nil {
		return nil, err
	}
	server := &Server{store: store, manager: NewManager(store, config.ArtifactRoot, config.DataRoot, config.Concurrency), auth: auth, secureCookie: config.SecureCookie}
	server.handler = server.routes()
	return server, nil
}

func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := s.manager.Shutdown(ctx)
	storeErr := s.store.Close()
	if shutdownErr != nil {
		return shutdownErr
	}
	return storeErr
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/auth/recover", s.handleRecover)
	mux.HandleFunc("GET /api/auth/session", s.handleSession)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /api/modules", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"modules": Modules()})
	})
	mux.HandleFunc("POST /api/preflight", s.handlePreflight)
	mux.HandleFunc("GET /api/jobs", s.handleListJobs)
	mux.HandleFunc("POST /api/jobs", s.handleCreateJob)
	mux.HandleFunc("GET /api/jobs/{id}/files", s.handleListJobFiles)
	mux.HandleFunc("GET /api/jobs/{id}/files/{path...}", s.handleGetJobFile)
	mux.HandleFunc("GET /api/jobs/{id}/download", s.handleDownloadJob)
	mux.HandleFunc("GET /api/jobs/{id}", s.handleGetJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.handleCancelJob)
	assets, _ := fs.Sub(webFiles, "web")
	fileServer := http.FileServer(http.FS(assets))
	mux.Handle("GET /", spaHandler(fileServer, assets))
	return securityHeaders(s.requireAuthentication(mux))
}

func (s *Server) requireAuthentication(next http.Handler) http.Handler {
	if s.auth == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/auth/") || (!strings.HasPrefix(r.URL.Path, "/api/") && (r.URL.Path == "/" || r.URL.Path == "/index.html" || r.URL.Path == "/styles.css" || r.URL.Path == "/app.js")) {
			next.ServeHTTP(w, r)
			return
		}
		session, err := s.auth.session(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		if err := s.auth.checkRemoteSession(session.User); err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	s.handleAuthAction(w, r, "verify-login", true)
}
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	s.handleAuthAction(w, r, "auth/register", true)
}

func (s *Server) handleRecover(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("未配置员工认证服务"))
		return
	}
	var request map[string]string
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.auth.authenticate("auth/forgot-password", request, false); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleAuthAction(w http.ResponseWriter, r *http.Request, action string, issueSession bool) {
	if s.auth == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("未配置员工认证服务"))
		return
	}
	var request map[string]string
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	user, err := s.auth.authenticate(action, request, true)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if issueSession {
		s.auth.issueSession(w, user, s.secureCookie)
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("未配置员工认证服务"))
		return
	}
	session, err := s.auth.session(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if err := s.auth.checkRemoteSession(session.User); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": session.User})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secureCookie})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type artifactFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	var request CreateJobRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.manager.Preflight(request.ModuleID, request.Input))
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	owner, err := s.ownerForRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	jobs, err := s.store.ListForOwner(owner.ID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) handleListJobFiles(w http.ResponseWriter, r *http.Request) {
	job, err := s.ownedJob(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	root, err := filepath.Abs(job.ArtifactPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("无法解析任务产物目录"))
		return
	}
	files := make([]artifactFile, 0)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, artifactFile{Path: filepath.ToSlash(relative), Size: info.Size()})
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("无法读取任务产物"))
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (s *Server) handleGetJobFile(w http.ResponseWriter, r *http.Request) {
	job, err := s.ownedJob(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	root, err := filepath.Abs(job.ArtifactPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("无法解析任务产物目录"))
		return
	}
	relative := filepath.Clean(filepath.FromSlash(r.PathValue("path")))
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		writeError(w, http.StatusBadRequest, errors.New("无效的任务文件路径"))
		return
	}
	target := filepath.Join(root, relative)
	contained, err := filepath.Rel(root, target)
	if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(os.PathSeparator)) {
		writeError(w, http.StatusBadRequest, errors.New("无效的任务文件路径"))
		return
	}
	file, err := os.Open(target)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("任务文件不存在"))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, errors.New("任务文件不存在"))
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(info.Name(), `"`, "")+`"`)
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var request CreateJobRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	owner, err := s.ownerForRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	job, err := s.manager.Submit(request.ModuleID, request.Input, owner)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.ownedJob(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	if _, err := s.ownedJob(r); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err := s.manager.Cancel(r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "cancelling"})
}

func (s *Server) ownerForRequest(r *http.Request) (JobOwner, error) {
	if s.auth == nil {
		return JobOwner{}, nil
	}
	session, err := s.auth.session(r)
	if err != nil {
		return JobOwner{}, err
	}
	return JobOwner{ID: session.User.ID, Name: session.User.Name}, nil
}

func (s *Server) ownedJob(r *http.Request) (Job, error) {
	owner, err := s.ownerForRequest(r)
	if err != nil {
		return Job{}, err
	}
	return s.store.GetForOwner(r.PathValue("id"), owner.ID)
}

func (s *Server) handleDownloadJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.ownedJob(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if job.Status != "succeeded" && job.Status != "partial" {
		writeError(w, http.StatusConflict, errors.New("任务完成后才能下载归档"))
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+job.ModuleID+`-`+job.ID+`.zip"`)
	archive := zip.NewWriter(w)
	err = filepath.WalkDir(job.ArtifactPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		relative, relErr := filepath.Rel(job.ArtifactPath, path)
		if relErr != nil {
			return relErr
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		defer file.Close()
		item, createErr := archive.Create(filepath.ToSlash(relative))
		if createErr != nil {
			return createErr
		}
		_, copyErr := io.Copy(item, file)
		return copyErr
	})
	closeErr := archive.Close()
	if err != nil || closeErr != nil {
		return
	}
}

func decodeJSON(r *http.Request, target any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid JSON request")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func spaHandler(fileServer http.Handler, assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(assets, path); err != nil {
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
