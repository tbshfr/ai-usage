// Command sanitize decodes an OTLP payload, redacts content/identity fields,
// and writes pretty OTLP/JSON to stdout.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/tbshfr/ai-usage/internal/sanitizeotel"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: sanitize <traces|metrics|logs> <file>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[2])
	if err != nil {
		fatal(err)
	}
	var output []byte
	switch os.Args[1] {
	case "traces":
		value, err := decodeTraces(data)
		if err != nil {
			fatal(err)
		}
		sanitizeotel.Traces(value)
		output, err = (&ptrace.JSONMarshaler{}).MarshalTraces(value)
		if err != nil {
			fatal(err)
		}
	case "metrics":
		value, err := decodeMetrics(data)
		if err != nil {
			fatal(err)
		}
		sanitizeotel.Metrics(value)
		output, err = (&pmetric.JSONMarshaler{}).MarshalMetrics(value)
		if err != nil {
			fatal(err)
		}
	case "logs":
		value, err := decodeLogs(data)
		if err != nil {
			fatal(err)
		}
		sanitizeotel.Logs(value)
		output, err = (&plog.JSONMarshaler{}).MarshalLogs(value)
		if err != nil {
			fatal(err)
		}
	default:
		fatal(fmt.Errorf("signal must be traces, metrics, or logs"))
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, output, "", "  "); err != nil {
		fatal(err)
	}
	pretty.WriteByte('\n')
	if _, err := os.Stdout.Write(pretty.Bytes()); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "sanitize:", err)
	os.Exit(1)
}

func decodeTraces(data []byte) (ptrace.Traces, error) {
	if looksJSON(data) {
		return (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(data)
	}
	return (&ptrace.ProtoUnmarshaler{}).UnmarshalTraces(data)
}

func decodeMetrics(data []byte) (pmetric.Metrics, error) {
	if looksJSON(data) {
		return (&pmetric.JSONUnmarshaler{}).UnmarshalMetrics(data)
	}
	return (&pmetric.ProtoUnmarshaler{}).UnmarshalMetrics(data)
}

func decodeLogs(data []byte) (plog.Logs, error) {
	if looksJSON(data) {
		return (&plog.JSONUnmarshaler{}).UnmarshalLogs(data)
	}
	return (&plog.ProtoUnmarshaler{}).UnmarshalLogs(data)
}

func looksJSON(data []byte) bool {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
}
