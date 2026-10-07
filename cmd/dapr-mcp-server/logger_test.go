package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNewLoggerWritesJSONToGivenWriter(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf).Info("hello", "key", "value")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log output is not JSON: %v: %q", err, buf.String())
	}
	if entry["msg"] != "hello" || entry["key"] != "value" {
		t.Errorf("unexpected log entry: %v", entry)
	}
}

func TestNewLoggerHonoursLevelEnv(t *testing.T) {
	t.Setenv(logLevelEnv, "error")

	var buf bytes.Buffer
	newLogger(&buf).Info("dropped")
	if buf.Len() != 0 {
		t.Errorf("info log written at error level: %q", buf.String())
	}
}
