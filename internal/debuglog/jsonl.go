package debuglog

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

type entry struct {
	TS      string          `json:"ts"`
	Source  string          `json:"source"`
	Prefix  string          `json:"prefix"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Text    string          `json:"text,omitempty"`
}

func AppendJSONL(path, source, prefix string, payload []byte) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}

	e := entry{
		TS:     time.Now().UTC().Format(time.RFC3339Nano),
		Source: source,
		Prefix: prefix,
	}
	if json.Valid(payload) {
		e.Payload = json.RawMessage(payload)
	} else {
		e.Text = string(payload)
	}

	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(line)
	return err
}
