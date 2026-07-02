package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

const (
	appName    = "hetzner-cloud-sync-server-agent"
	appVersion = "dev"
	jsonLimit  = 1 << 20
)

type config struct {
	ListenAddr   string
	Token        string
	Endpoint     string
	PollInterval time.Duration
}

type app struct {
	client       *hcloud.Client
	pollInterval time.Duration
}

type deleteRequest struct {
	Server     string `json:"server"`
	ServerID   int64  `json:"server_id"`
	ServerName string `json:"server_name"`
}

type deleteResponse struct {
	ServerID   int64  `json:"server_id"`
	ServerName string `json:"server_name"`
	ActionID   int64  `json:"action_id"`
	Status     string `json:"status"`
	Message    string `json:"message"`
}

type runningServerResponse struct {
	ServerID   int64     `json:"server_id"`
	ServerName string    `json:"server_name"`
	Status     string    `json:"status"`
	Created    time.Time `json:"created"`
}

type httpError struct {
	Status  int    `json:"-"`
	Message string `json:"message"`
}

func (e httpError) Error() string {
	return e.Message
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	options := []hcloud.ClientOption{
		hcloud.WithToken(cfg.Token),
		hcloud.WithApplication(appName, appVersion),
	}
	if cfg.Endpoint != "" {
		options = append(options, hcloud.WithEndpoint(cfg.Endpoint))
	}

	client := hcloud.NewClient(options...)
	service := &app{
		client:       client,
		pollInterval: cfg.PollInterval,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", service.handleHealth)
	mux.HandleFunc("/servers/running", service.handleListRunningServers)
	mux.HandleFunc("/delete", service.handleDelete)
	mux.HandleFunc("/servers/", service.handleDeleteServer)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-shutdownCtx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}()

	log.Printf("listening on %s", cfg.ListenAddr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}

func loadConfig() (config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return config{}, err
	}

	cfg := config{
		ListenAddr:   envOrDefault("LISTEN_ADDR", ":8080"),
		Token:        strings.TrimSpace(os.Getenv("HETZNER_TOKEN")),
		Endpoint:     strings.TrimSpace(os.Getenv("HETZNER_ENDPOINT")),
		PollInterval: mustDuration(envOrDefault("HETZNER_POLL_INTERVAL", "2s")),
	}

	if cfg.Token == "" {
		return config{}, httpError{Status: http.StatusBadRequest, Message: "HETZNER_TOKEN is required"}
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	return cfg, nil
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			}
		} else if len(value) >= 2 && strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
			value = value[1 : len(value)-1]
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func (a *app) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) handleListRunningServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	servers, err := a.listRunningServers(r.Context())
	if err != nil {
		writeHTTPError(w, fmt.Errorf("list running servers: %w", err))
		return
	}

	response := make([]runningServerResponse, 0, len(servers))
	for _, server := range servers {
		if server == nil {
			continue
		}
		response = append(response, runningServerResponse{
			ServerID:   server.ID,
			ServerName: server.Name,
			Status:     string(server.Status),
			Created:    server.Created,
		})
	}

	writeJSON(w, http.StatusOK, response)
}

func (a *app) listRunningServers(ctx context.Context) ([]*hcloud.Server, error) {
	return a.client.Server.AllWithOpts(ctx, hcloud.ServerListOpts{
		Status: []hcloud.ServerStatus{hcloud.ServerStatusRunning},
	})
}

func (a *app) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	selector := strings.TrimPrefix(r.URL.Path, "/servers/")
	selector = strings.Trim(selector, "/")
	if selector == "" {
		writeError(w, http.StatusBadRequest, "server id or name is required")
		return
	}

	a.deleteServerRequest(w, r, selector)
}

func (a *app) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	selector, err := selectorFromRequest(w, r)
	if err != nil {
		writeHTTPError(w, err)
		return
	}

	a.deleteServerRequest(w, r, selector)
}

func selectorFromRequest(w http.ResponseWriter, r *http.Request) (string, error) {
	if selector := strings.TrimSpace(r.URL.Query().Get("server")); selector != "" {
		return selector, nil
	}
	if selector := strings.TrimSpace(r.URL.Query().Get("server_id")); selector != "" {
		return selector, nil
	}
	if selector := strings.TrimSpace(r.URL.Query().Get("server_name")); selector != "" {
		return selector, nil
	}

	if r.Method == http.MethodDelete {
		return "", httpError{Status: http.StatusBadRequest, Message: "server selector is required"}
	}

	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, jsonLimit)
	var req deleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", httpError{Status: http.StatusBadRequest, Message: "invalid JSON body"}
	}

	if selector := strings.TrimSpace(req.Server); selector != "" {
		return selector, nil
	}
	if req.ServerID != 0 {
		return strconv.FormatInt(req.ServerID, 10), nil
	}
	if selector := strings.TrimSpace(req.ServerName); selector != "" {
		return selector, nil
	}

	return "", httpError{Status: http.StatusBadRequest, Message: "server, server_id, or server_name is required"}
}

func (a *app) deleteServerRequest(w http.ResponseWriter, r *http.Request, selector string) {
	ctx := r.Context()

	server, _, err := a.client.Server.Get(ctx, selector)
	if err != nil {
		writeHTTPError(w, fmt.Errorf("lookup server %q: %w", selector, err))
		return
	}
	if server == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("server %q not found", selector))
		return
	}
	if server.Status != hcloud.ServerStatusRunning {
		writeError(w, http.StatusConflict, fmt.Sprintf("server %q is not running", server.Name))
		return
	}
	if server.Protection.Delete {
		writeError(w, http.StatusConflict, fmt.Sprintf("server %q has delete protection enabled", server.Name))
		return
	}

	result, _, err := a.client.Server.DeleteWithResult(ctx, server)
	if err != nil {
		writeHTTPError(w, fmt.Errorf("delete server %q: %w", server.Name, err))
		return
	}
	if result.Action == nil {
		writeError(w, http.StatusInternalServerError, "delete action was not returned")
		return
	}

	finishedAction, err := waitForAction(ctx, a.client, result.Action, a.pollInterval)
	if err != nil {
		writeHTTPError(w, fmt.Errorf("wait for delete action %d: %w", result.Action.ID, err))
		return
	}
	result.Action = finishedAction

	writeJSON(w, http.StatusOK, deleteResponse{
		ServerID:   server.ID,
		ServerName: server.Name,
		ActionID:   result.Action.ID,
		Status:     string(result.Action.Status),
		Message:    "server deleted",
	})
}

func waitForAction(ctx context.Context, client *hcloud.Client, action *hcloud.Action, interval time.Duration) (*hcloud.Action, error) {
	if action == nil {
		return nil, errors.New("missing action")
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	current := action
	for {
		if err := current.Error(); err != nil {
			return current, err
		}
		switch current.Status {
		case hcloud.ActionStatusSuccess:
			return current, nil
		case hcloud.ActionStatusError:
			return current, current.Error()
		}

		select {
		case <-ctx.Done():
			return current, ctx.Err()
		case <-ticker.C:
		}

		updated, _, err := client.Action.GetByID(ctx, current.ID)
		if err != nil {
			return current, err
		}
		if updated == nil {
			return current, fmt.Errorf("action %d not found", current.ID)
		}
		current = updated
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func writeHTTPError(w http.ResponseWriter, err error) {
	var he httpError
	if errors.As(err, &he) {
		writeError(w, he.Status, he.Message)
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, httpError{Status: status, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func mustDuration(value string) time.Duration {
	if value == "" {
		return 0
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		log.Fatalf("invalid duration %q: %v", value, err)
	}
	return duration
}
