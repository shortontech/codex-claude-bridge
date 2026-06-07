package server

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shortontech/codex-claude-bridge/internal/config"
)

func TestHandleMessagesStreamForwardsTextDeltasBeforeUpstreamCompletes(t *testing.T) {
	firstUpstreamDeltaSent := make(chan struct{})
	releaseUpstream := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("upstream recorder does not support flushing")
		}

		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\",\"output_index\":0}\n\n")
		flusher.Flush()
		close(firstUpstreamDeltaSent)

		select {
		case <-releaseUpstream:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for test to release upstream")
		}

		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\" world\",\"output_index\":0}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-test\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":0}},\"output\":[]}}\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	bridge := httptest.NewServer(New(config.Config{
		OpenAIAPIKey:        "test-token",
		OpenAIBase:          upstream.URL,
		OpenAIResponsesPath: "/responses",
		DefaultInstructions: "test instructions",
		DefaultModel:        "gpt-test",
		HaikuModel:          "gpt-test-haiku",
	}).Routes())
	defer bridge.Close()

	body := bytes.NewBufferString(`{
		"model":"gpt-test",
		"max_tokens":128,
		"stream":true,
		"messages":[{"role":"user","content":[{"type":"text","text":"Say hello"}]}]
	}`)
	req, err := http.NewRequest(http.MethodPost, bridge.URL+"/v1/messages", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %s", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream response, got %q", ct)
	}

	select {
	case <-firstUpstreamDeltaSent:
	case <-time.After(2 * time.Second):
		t.Fatal("fake upstream did not send first delta")
	}

	lines := make(chan string, 32)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	var sawLiveDelta bool
	deadline := time.After(2 * time.Second)
	for !sawLiveDelta {
		select {
		case line := <-lines:
			if strings.Contains(line, `"text":"Hello"`) {
				sawLiveDelta = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for live text delta before upstream completed")
		}
	}

	close(releaseUpstream)

	var rest []string
	for line := range lines {
		rest = append(rest, line)
	}
	<-readDone

	fullStream := strings.Join(rest, "\n")
	if strings.Contains(fullStream, "[DONE]") {
		t.Fatal("anthropic stream should not include a raw [DONE] trailer")
	}
	if !strings.Contains(fullStream, "message_stop") {
		t.Fatal("stream did not finish with message_stop")
	}
}
