package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testKey = "0123456789abcdef0123456789abcdef"

func testService(t *testing.T) *service {
	t.Helper()
	s, err := newService(testKey, "/missing", 1, 10000)
	if err != nil {
		t.Fatal(err)
	}
	s.ready = func() bool { return true }
	s.convert = func(context.Context, string) (string, error) { return "gujarat", nil }
	return s
}
func request(s *service, method, path, body, key, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", key)
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func post(s *service, body string) *httptest.ResponseRecorder {
	return request(s, "POST", "/v1/reverse", body, "Bearer "+testKey, "application/json")
}

func TestContract(t *testing.T) {
	s := testService(t)
	for _, tc := range []struct {
		method, path, body, key, content string
		status                           int
	}{
		{"GET", "/health", "", "", "", 200},
		{"POST", "/v1/reverse", `{"text":"ગુજરાત"}`, "Bearer " + testKey, "application/json", 200},
		{"POST", "/v1/reverse", `{"text":"ગુજરાત"}`, "wrong", "application/json", 401},
		{"POST", "/v1/reverse", `{`, "Bearer " + testKey, "application/json", 400},
		{"POST", "/v1/reverse", `{}`, "Bearer " + testKey, "application/json", 400},
		{"POST", "/v1/reverse", `{"text":3}`, "Bearer " + testKey, "application/json", 400},
		{"POST", "/v1/reverse", `{"text":"ગુજરાત"}`, "Bearer " + testKey, "text/plain", 415},
		{"GET", "/v1/reverse", "", "", "", 405},
		{"POST", "/unknown", "", "", "", 404},
		{"POST", "/v1/reverse", strings.Repeat("x", bodyLimit+1), "Bearer " + testKey, "application/json", 413},
	} {
		w := request(s, tc.method, tc.path, tc.body, tc.key, tc.content)
		if w.Code != tc.status {
			t.Fatalf("%s %s: got %d want %d: %s", tc.method, tc.path, w.Code, tc.status, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("Missing security header")
		}
	}
	s.ready = func() bool { return false }
	if post(s, `{"text":"test"}`).Code != 503 || request(s, "GET", "/health", "", "", "").Code != 503 {
		t.Fatal("Readiness failure not reported")
	}
}

func TestValidationAndParser(t *testing.T) {
	for _, text := range []string{"", " ", "a\x00b", "a\rb", "a\x85b", strings.Repeat("अ", 4097), strings.Repeat("a ", 129), "words.CSV", "/tmp/input.csv", string([]byte{0xff})} {
		if _, err := wordsFor(text); err == nil {
			t.Fatalf("Accepted invalid input %q", text)
		}
	}
	text := "  मेरा\tनाम\n"
	words, _ := wordsFor(text)
	output, err := parseOutput(text, words, "मेरा\tmera\nनाम\tnaam\n")
	if err != nil || output != "  mera\tnaam\n" {
		t.Fatalf("Whitespace changed: %q %v", output, err)
	}
	for _, output := range []string{"", "other\tvalue\n", "मेरा\tmera\n", "मेरा\tmera\textra\nनाम\tnaam\n"} {
		if _, err := parseOutput(text, words, output); err == nil {
			t.Fatal("Accepted malformed output")
		}
	}
}

func TestWorkerBoundAndRelease(t *testing.T) {
	s := testService(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan *httptest.ResponseRecorder)
	s.convert = func(context.Context, string) (string, error) {
		close(entered)
		<-release
		return "", fail(504, "Timed out")
	}
	go func() { done <- post(s, `{"text":"test"}`) }()
	<-entered
	w := post(s, `{"text":"test"}`)
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("Overload not rejected")
	}
	close(release)
	if (<-done).Code != 504 {
		t.Fatal("Timeout lost")
	}
	s.convert = func(context.Context, string) (string, error) { return "ok", nil }
	if post(s, `{"text":"test"}`).Code != 200 {
		t.Fatal("Worker not released")
	}
	for i := 0; i < cap(s.requests); i++ {
		s.requests <- struct{}{}
	}
	if post(s, `{"text":"test"}`).Code != 503 {
		t.Fatal("HTTP request bound not enforced")
	}
}

func TestRateLimitAndConfiguration(t *testing.T) {
	if _, err := newService("short", "", 1, 1); err == nil {
		t.Fatal("Weak key accepted")
	}
	if _, err := newService(testKey, "", 9, 1); err == nil {
		t.Fatal("Unbounded workers accepted")
	}
	s, _ := newService(testKey, "", 1, 1)
	w := request(s, "POST", "/v1/reverse", "", "wrong", "application/json")
	if w.Code != 401 {
		t.Fatal("Authentication failed")
	}
	w = post(s, `{"text":"test"}`)
	if w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("Rate limit failed")
	}
}

func TestProcessRunner(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "binary")
	write := func(script string) {
		t.Helper()
		if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write("[ \"$1,$2,$3\" = '-top,1,--' ] || exit 1\nshift 3\nfor word in \"$@\"; do printf '%s\\t%s\\n' \"$word\" \"$word\"; done\n")
	text := "-flag $(touch) ગુજરાત"
	got, err := runBinary(context.Background(), binary, text, time.Second)
	if err != nil || got != text {
		t.Fatalf("Arguments changed: %q %v", got, err)
	}
	write("exec sleep 30\n")
	_, err = runBinary(context.Background(), binary, "test", 100*time.Millisecond)
	if e, ok := err.(*apiError); !ok || e.status != 504 {
		t.Fatalf("Timeout not enforced: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runBinary(ctx, binary, "test", time.Second); err != context.Canceled {
		t.Fatalf("Cancellation not propagated: %v", err)
	}
	if _, err := runBinary(context.Background(), binary+"missing", "test", time.Second); err == nil {
		t.Fatal("Missing binary accepted")
	}
	write("printf 'bad output'\n")
	if _, err := runBinary(context.Background(), binary, "test", time.Second); err == nil {
		t.Fatal("Bad output accepted")
	}
	buffer := &limitedBuffer{limit: 3}
	if _, err := buffer.Write([]byte("1234")); err == nil || buffer.Len() != 0 {
		t.Fatal("Output bound failed")
	}
}

func TestHTTPClientCancellation(t *testing.T) {
	s := testService(t)
	entered, canceled := make(chan struct{}), make(chan struct{})
	s.convert = func(ctx context.Context, _ string) (string, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		return "", ctx.Err()
	}
	server := httptest.NewServer(s)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/reverse", strings.NewReader(`{"text":"test"}`))
	r.Header.Set("Authorization", "Bearer "+testKey)
	r.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		response, _ := http.DefaultClient.Do(r)
		if response != nil {
			response.Body.Close()
		}
		close(done)
	}()
	<-entered
	cancel()
	<-done
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Client cancellation not propagated")
	}
}

func TestRealBinary(t *testing.T) {
	binary := os.Getenv("TEST_REAL_BINARY")
	if binary == "" {
		t.Skip("Set TEST_REAL_BINARY for native integration test")
	}
	s, err := newService(testKey, binary, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	w := post(s, `{"text":"  ગુજરાત\tमेरा नाम राहुल है தமிழ் తెలుగు বাংলা ଓଡ଼ିଆ मराठी ಕನ್ನಡ മലയാളം অসমীয়া ਪੰਜਾਬੀ\n"}`)
	if w.Code != 200 {
		t.Fatalf("Binary failed: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result != "  gujarat\tmera naam rahul hai tamil telugu bangla oriya marathi kannada malayalam asamiya punjabi\n" {
		t.Fatalf("Unexpected conversion: %q", response.Result)
	}
}
