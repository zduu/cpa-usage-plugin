package main

// Streaming, schema-aware decoding of verified legacy sources. Output is an
// ordered stream, not live request mutations. A consumer must finish parsing
// successfully before activating anything, then apply the legacy snapshot /
// replay reconciliation rules. In particular, aggregates are not requests.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

type sqliteMigrationItem struct {
	// Path uses canonical struct field names, but preserves API/model/map
	// keys exactly. Array indices are decimal path components. Begin/end and
	// null events preserve empty containers and duplicate-member ordering.
	Path       []string
	Kind       string // object, array, end, value, null, invalid
	Start, End int64  // Byte offsets in the original source (end is exclusive).
	Value      json.RawMessage
}

type sqliteMigrationParseResult struct {
	Version     int
	GeneratedAt time.Time
	// JSONL physical rows, not deduplicated requests or snapshot totals.
	Requests, Metadata, Invalid int64
}

// ScanMigrationSource retains one atomic record/scalar at a time, not a whole
// model, time-series array, API dictionary or snapshot. Memory is proportional
// to the largest individual value plus parser buffers; it is not a hard RSS
// bound for arbitrarily large legacy records. Raw typed JSON retains int64
// precision. Consumers must not accumulate this stream in memory in production.
func (s *sqliteLedger) ScanMigrationSource(parent context.Context, path string, consume func(sqliteMigrationItem) error) (result sqliteMigrationParseResult, err error) {
	defer func() {
		if err != nil && parent.Err() != nil {
			err = parent.Err()
		}
	}()
	if consume == nil {
		return result, errors.New("sqlite migration requires a consumer")
	}
	ctx, cancel := s.operationContext(parent, 0)
	defer cancel()
	path, err = canonicalSQLiteMigrationPath(path)
	if err != nil {
		return result, err
	}
	source, err := s.migrationSource(ctx, path)
	if err != nil {
		return result, err
	}
	err = s.WithStagedSource(ctx, path, func(reader io.Reader) error {
		if source.Format == "jsonl" {
			var scanErr error
			result, scanErr = scanSQLiteMigrationJSONL(ctx, reader, consume)
			return scanErr
		}
		if source.Format != "snapshot" {
			return errors.New("unsupported sqlite migration source format")
		}
		parser := sqliteSnapshotParser{ctx: ctx, decoder: json.NewDecoder(reader), consume: consume}
		parser.decoder.UseNumber()
		if err := parser.object(nil, "snapshot"); err != nil {
			return err
		}
		// Force an EOF read, both to reject trailing JSON and to verify the
		// staged reader checksum. Decoder may otherwise stop before EOF.
		if _, err := parser.decoder.Token(); !errors.Is(err, io.EOF) {
			if err == nil {
				return errors.New("unexpected trailing snapshot JSON")
			}
			return err
		}
		result = parser.result
		generatedAt, err := validateStorageSnapshotHeader(result.Version, parser.generatedAt)
		if err != nil {
			return err
		}
		result.GeneratedAt = generatedAt
		return nil
	})
	return result, err
}

func scanSQLiteMigrationJSONL(ctx context.Context, reader io.Reader, consume func(sqliteMigrationItem) error) (result sqliteMigrationParseResult, err error) {
	lines := bufio.NewReaderSize(reader, 64<<10)
	var offset, line int64
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		raw, readErr := lines.ReadBytes('\n')
		start := offset
		offset += int64(len(raw))
		if len(raw) > 0 {
			line++
			if len(bytes.TrimSpace(raw)) > 0 {
				var record persistedDetail
				decodeErr := json.Unmarshal(raw, &record)
				item := sqliteMigrationItem{Path: []string{strconv.FormatInt(line, 10)}, Kind: "value", Start: start, End: offset, Value: raw}
				if decodeErr != nil || strings.TrimSpace(record.API) == "" {
					item.Kind = "invalid"
					result.Invalid++
				} else if record.MetadataOnly {
					result.Metadata++
				} else {
					result.Requests++
				}
				if err := consume(item); err != nil {
					return result, err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return result, nil
		}
		if readErr != nil {
			return result, readErr
		}
	}
}

type sqliteSnapshotParser struct {
	ctx         context.Context
	decoder     *json.Decoder
	consume     func(sqliteMigrationItem) error
	result      sqliteMigrationParseResult
	generatedAt string
}

func (p *sqliteSnapshotParser) emit(path []string, kind string, start int64, value json.RawMessage) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	return p.consume(sqliteMigrationItem{Path: append([]string(nil), path...), Kind: kind, Start: start, End: p.decoder.InputOffset(), Value: value})
}

func (p *sqliteSnapshotParser) object(path []string, scope string) error {
	start := p.decoder.InputOffset()
	token, err := p.decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return p.emit(path, "null", p.decoder.InputOffset()-4, nil)
	}
	if token != json.Delim('{') {
		return fmt.Errorf("expected %s object at byte %d", scope, start)
	}
	start = p.decoder.InputOffset() - 1
	if err := p.emit(path, "object", start, nil); err != nil {
		return err
	}
	for p.decoder.More() {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		token, err := p.decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("snapshot object key is not a string")
		}
		field := sqliteMigrationFieldName(key)
		if scope == "apis" || scope == "models" || strings.HasPrefix(scope, "map:") {
			field = key // Map keys are identities, not struct field names.
		}
		child := append(append([]string(nil), path...), field)
		switch {
		case scope == "snapshot" && field == "usage":
			err = p.object(child, "usage")
		case scope == "snapshot" && (field == "version" || field == "generated_at"):
			err = p.value(child, field)
		case scope == "usage" && field == "apis":
			err = p.object(child, "apis")
		case scope == "apis":
			err = p.object(child, "api")
		case scope == "api" && field == "models":
			err = p.object(child, "models")
		case scope == "models":
			err = p.object(child, "model")
		case scope == "model" && (field == "details" || field == "accounting"):
			err = p.array(child, "detail")
		case scope == "model" && field == "providers":
			err = p.array(child, "provider")
		case scope == "usage" && (field == "cost_tokens_by_day" || field == "cost_tokens_by_hour"):
			err = p.object(child, "map:tokens")
		case scope == "usage" && (field == "requests_by_day" || field == "requests_by_hour" || field == "tokens_by_day" || field == "tokens_by_hour"):
			err = p.object(child, "map:count")
		case scope == "usage" && (field == "cost_by_day" || field == "cost_by_hour"):
			err = p.object(child, "map:cost")
		case scope == "map:tokens":
			err = p.array(child, "tokens")
		case scope == "map:count":
			err = p.value(child, "count")
		case scope == "map:cost":
			err = p.value(child, "float")
		case (scope == "usage" || scope == "api" || scope == "model") && field == "avg_latency_ms":
			err = p.value(child, "float")
		case (scope == "usage" || scope == "api" || scope == "model") && sqliteMigrationCountField(field):
			err = p.value(child, "count")
		default:
			err = p.skipValue(0) // Unknown fields are ignored by the old typed decoder too.
		}
		if err != nil {
			return err
		}
	}
	if token, err := p.decoder.Token(); err != nil || token != json.Delim('}') {
		return fmt.Errorf("invalid snapshot object end at byte %d: %v", p.decoder.InputOffset(), err)
	}
	return p.emit(path, "end", start, nil)
}

func sqliteMigrationCountField(field string) bool {
	switch field {
	case "total_requests", "success_count", "failure_count", "total_tokens", "input_tokens", "output_tokens", "cached_tokens", "cache_write_tokens", "reasoning_tokens":
		return true
	}
	return false
}

func sqliteMigrationFieldName(key string) string {
	// Match encoding/json's Unicode case folding too (for example long-s
	// in "ſuccess_count"). ToLower alone silently drops such known fields.
	for _, field := range [...]string{
		"version", "generated_at", "usage", "apis", "models", "details", "accounting", "providers",
		"cost_tokens_by_day", "cost_tokens_by_hour", "requests_by_day", "requests_by_hour",
		"tokens_by_day", "tokens_by_hour", "cost_by_day", "cost_by_hour", "avg_latency_ms",
		"total_requests", "success_count", "failure_count", "total_tokens", "input_tokens",
		"output_tokens", "cached_tokens", "cache_write_tokens", "reasoning_tokens",
	} {
		if strings.EqualFold(key, field) {
			return field
		}
	}
	return key
}

func (p *sqliteSnapshotParser) array(path []string, kind string) error {
	start := p.decoder.InputOffset()
	token, err := p.decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return p.emit(path, "null", p.decoder.InputOffset()-4, nil)
	}
	if token != json.Delim('[') {
		return fmt.Errorf("expected snapshot array at byte %d", start)
	}
	start = p.decoder.InputOffset() - 1
	if err := p.emit(path, "array", start, nil); err != nil {
		return err
	}
	for index := int64(0); p.decoder.More(); index++ {
		child := append(append([]string(nil), path...), strconv.FormatInt(index, 10))
		if err := p.value(child, kind); err != nil {
			return err
		}
	}
	if token, err := p.decoder.Token(); err != nil || token != json.Delim(']') {
		return fmt.Errorf("invalid snapshot array end at byte %d: %v", p.decoder.InputOffset(), err)
	}
	return p.emit(path, "end", start, nil)
}

func (p *sqliteSnapshotParser) value(path []string, kind string) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	start := p.decoder.InputOffset()
	var raw json.RawMessage
	if err := p.decoder.Decode(&raw); err != nil {
		return err
	}
	// InputOffset before Decode is before ':' / ',' and whitespace. Derive
	// the exact raw-value start from its end, so stored spans can be reread.
	start = p.decoder.InputOffset() - int64(len(raw))
	// Validate with the same types used by legacy json.Unmarshal. Do not
	// normalize, reprice, deduplicate or apply v1 cache conversion here: version
	// may occur after usage, and repeated fields retain their source ordering.
	var target any
	switch kind {
	case "version":
		target = &p.result.Version
	case "generated_at":
		target = &p.generatedAt
	case "count":
		target = new(int64)
	case "float":
		target = new(float64)
	case "detail":
		target = new(RequestDetail)
	case "provider":
		target = new(ModelProviderStat)
	case "tokens":
		target = new(TimeSeriesTokenStat)
	default:
		return errors.New("unknown snapshot value kind")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode snapshot %s at byte %d: %w", kind, start, err)
	}
	return p.emit(path, "value", start, raw)
}

func (p *sqliteSnapshotParser) skipValue(depth int) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if depth > 10000 {
		return errors.New("snapshot JSON exceeds maximum nesting depth")
	}
	token, err := p.decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		for p.decoder.More() {
			if _, err := p.decoder.Token(); err != nil {
				return err
			}
			if err := p.skipValue(depth + 1); err != nil {
				return err
			}
		}
		_, err = p.decoder.Token()
	case json.Delim('['):
		for p.decoder.More() {
			if err := p.skipValue(depth + 1); err != nil {
				return err
			}
		}
		_, err = p.decoder.Token()
	case json.Delim('}'), json.Delim(']'):
		return errors.New("unexpected snapshot closing delimiter")
	}
	return err
}
