package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

const bodyLimit = 16 * 1024

type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string           { return e.message }
func fail(status int, message string) error { return &apiError{status, message} }

func wordsFor(text string) ([]string, error) {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 4096 {
		return nil, fail(400, "text must contain 1–4096 Unicode characters")
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\t' && r != '\n' {
			return nil, fail(400, "Control characters are not supported")
		}
	}
	words := strings.Fields(text)
	if len(words) > 128 {
		return nil, fail(400, "text must contain at most 128 words")
	}
	for _, word := range words {
		// The supplied executable interprets CSV arguments as filenames.
		if strings.HasSuffix(strings.ToLower(word), ".csv") {
			return nil, fail(400, "CSV filenames are not supported")
		}
	}
	return words, nil
}

func parseOutput(text string, words []string, stdout string) (string, error) {
	if !utf8.ValidString(stdout) {
		return "", fail(502, "Invalid binary response")
	}
	lines := strings.Split(strings.TrimSuffix(strings.TrimSuffix(stdout, "\n"), "\r"), "\n")
	if len(lines) != len(words) {
		return "", fail(502, "Invalid binary response")
	}
	results := make([]string, len(words))
	for i, line := range lines {
		fields := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
		if len(fields) != 2 || fields[0] != words[i] || fields[1] == "" {
			return "", fail(502, "Invalid binary response")
		}
		for _, r := range fields[1] {
			if unicode.IsControl(r) {
				return "", fail(502, "Invalid binary response")
			}
		}
		results[i] = fields[1]
	}
	var output strings.Builder
	index, inWord := 0, false
	for _, r := range text {
		if unicode.IsSpace(r) {
			output.WriteRune(r)
			inWord = false
		} else if !inWord {
			output.WriteString(results[index])
			index++
			inWord = true
		}
	}
	return output.String(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("Binary output limit exceeded")
	}
	return b.Buffer.Write(p)
}

func runBinary(ctx context.Context, binary, text string, timeout time.Duration) (string, error) {
	words, err := wordsFor(text)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, append([]string{"-top", "1", "--"}, words...)...)
	stdout := &limitedBuffer{limit: 65536}
	stderr := &limitedBuffer{limit: 65536}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fail(504, "Transliteration timed out")
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fail(502, "Transliteration failed")
	}
	return parseOutput(text, words, stdout.String())
}

type service struct {
	key      [32]byte
	binary   string
	workers  chan struct{}
	requests chan struct{}
	convert  func(context.Context, string) (string, error)
	ready    func() bool
	mu       sync.Mutex
	tokens   float64
	last     time.Time
	rate     float64
}

func newService(key, binary string, workers, rate int) (*service, error) {
	if len(key) < 32 {
		return nil, errors.New("API_KEY must contain at least 32 bytes")
	}
	if workers < 1 || workers > 8 || rate < 1 || rate > 10000 {
		return nil, errors.New("WORKERS must be 1–8 and RATE_LIMIT_RPS must be 1–10000")
	}
	s := &service{key: sha256.Sum256([]byte("Bearer " + key)), binary: binary, workers: make(chan struct{}, workers), requests: make(chan struct{}, 64), tokens: float64(rate), last: time.Now(), rate: float64(rate)}
	s.ready = func() bool {
		info, err := os.Stat(binary)
		return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
	}
	s.convert = func(ctx context.Context, text string) (string, error) {
		return runBinary(ctx, binary, text, 15*time.Second)
	}
	return s, nil
}

func (s *service) allow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.tokens = min(s.rate, s.tokens+now.Sub(s.last).Seconds()*s.rate)
	s.last = now
	if s.tokens < 1 {
		return false
	}
	s.tokens--
	return true
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if status == 429 || status == 503 {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func replyError(w http.ResponseWriter, err error) {
	var api *apiError
	if errors.As(err, &api) {
		reply(w, api.status, map[string]string{"error": api.message})
	} else {
		reply(w, 500, map[string]string{"error": "Internal server error"})
	}
}

func (s *service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" && r.Method == http.MethodGet {
		if s.ready() {
			reply(w, 200, map[string]string{"status": "ok"})
		} else {
			reply(w, 503, map[string]string{"status": "unavailable"})
		}
		return
	}
	if r.URL.Path != "/v1/reverse" {
		replyError(w, fail(404, "Not found"))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		replyError(w, fail(405, "Use POST"))
		return
	}
	if !s.allow() {
		replyError(w, fail(429, "Rate limit exceeded"))
		return
	}
	key := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	if subtle.ConstantTimeCompare(key[:], s.key[:]) != 1 {
		replyError(w, fail(401, "Unauthorized"))
		return
	}
	select {
	case s.requests <- struct{}{}:
		defer func() { <-s.requests }()
	default:
		replyError(w, fail(503, "Too many requests"))
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		replyError(w, fail(415, "Use application/json"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var size *http.MaxBytesError
		if errors.As(err, &size) {
			replyError(w, fail(413, "Request body too large"))
		} else {
			replyError(w, fail(400, "Invalid body"))
		}
		return
	}
	var payload struct {
		Text string `json:"text"`
	}
	if !utf8.Valid(body) || json.Unmarshal(body, &payload) != nil {
		replyError(w, fail(400, "Invalid JSON"))
		return
	}
	if _, err := wordsFor(payload.Text); err != nil {
		replyError(w, err)
		return
	}
	if !s.ready() {
		replyError(w, fail(503, "Binary unavailable"))
		return
	}
	// No unbounded queue or automatic retries: each process needs roughly 470 MiB.
	select {
	case s.workers <- struct{}{}:
		defer func() { <-s.workers }()
	default:
		replyError(w, fail(503, "Service busy; retry later"))
		return
	}
	result, err := s.convert(r.Context(), payload.Text)
	if err != nil {
		replyError(w, err)
		return
	}
	reply(w, 200, map[string]string{"result": result})
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		client := &http.Client{Timeout: 2 * time.Second}
		response, err := client.Get("http://127.0.0.1:" + env("PORT", "4100") + "/health")
		if err != nil {
			os.Exit(1)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	workers, err := strconv.Atoi(env("WORKERS", "1"))
	if err != nil {
		log.Fatal("Invalid WORKERS")
	}
	rate, err := strconv.Atoi(env("RATE_LIMIT_RPS", "100"))
	if err != nil {
		log.Fatal("Invalid RATE_LIMIT_RPS")
	}
	binary, err := filepath.Abs(env("BINARY_PATH", "./bin/translit-reverse"))
	if err != nil {
		log.Fatal(err)
	}
	s, err := newService(os.Getenv("API_KEY"), binary, workers, rate)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: env("HOST", "127.0.0.1") + ":" + env("PORT", "4100"), Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if server.Shutdown(shutdownCtx) != nil {
			_ = server.Close()
		}
		close(done)
	}()
	fmt.Printf("Reverse transliteration listening on %s (workers=%d)\n", server.Addr, workers)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	stop()
	<-done
}
