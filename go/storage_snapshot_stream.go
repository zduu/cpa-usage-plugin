package main

import (
	"bufio"
	"encoding/json"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"
)

type storageSnapshotModelKey struct{ API, Model string }

type storageSnapshotView struct {
	metadata StatisticsSnapshot
	archived map[storageSnapshotModelKey][][]accountingRecord
}

func (s *RequestStatistics) captureStorageSnapshot() storageSnapshotView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captureStorageSnapshotLocked()
}

func (s *RequestStatistics) captureStorageSnapshotLocked() storageSnapshotView {
	view := storageSnapshotView{metadata: s.snapshotWithAccountingLocked(false), archived: make(map[storageSnapshotModelKey][][]accountingRecord)}
	for api, apiStats := range s.apis {
		for model, modelStats := range apiStats.Models {
			if blocks := modelStats.accounting.freeze(); len(blocks) > 0 {
				view.archived[storageSnapshotModelKey{api, model}] = blocks
			}
		}
	}
	return view
}

// Only transient representations override the nested JSON fields; the public
// snapshot structs and their import/export contracts remain unchanged.
type storageUsageHeader struct {
	*StatisticsSnapshot
	APIs *int `json:"apis,omitempty"`
}
type storageAPIHeader struct {
	*APISnapshot
	Models *int `json:"models,omitempty"`
}
type storageModelHeader struct {
	*ModelSnapshot
	Details    *int `json:"details,omitempty"`
	Accounting *int `json:"accounting,omitempty"`
}

func writeJSONObjectPrefix(w io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = w.Write(raw[:len(raw)-1]) // leave the object open for streamed children
	return err
}

func (view storageSnapshotView) write(w io.Writer, now time.Time) error {
	header := struct {
		Version     int    `json:"version"`
		GeneratedAt string `json:"generated_at"`
	}{currentStorageSnapshotVersion, now.UTC().Format(time.RFC3339)}
	if err := writeJSONObjectPrefix(w, header); err != nil {
		return err
	}
	if _, err := io.WriteString(w, `,"usage":`); err != nil {
		return err
	}
	if err := view.writeUsage(w); err != nil {
		return err
	}
	_, err := io.WriteString(w, "}")
	return err
}

// Shared by storage v2 snapshots and the public v1 usage backup. Neither
// caller expands the archived ledger or buffers the complete JSON document.
func (view storageSnapshotView) writeUsage(w io.Writer) error {
	if err := writeJSONObjectPrefix(w, storageUsageHeader{StatisticsSnapshot: &view.metadata}); err != nil {
		return err
	}
	if _, err := io.WriteString(w, `,"apis":{`); err != nil {
		return err
	}
	encoder := json.NewEncoder(w)
	// Encoder.Encode completes synchronously. Reuse one expanded archive row
	// instead of escaping a new RequestDetail through interface{} for each row.
	var archivedDetail RequestDetail
	for apiIndex, apiName := range slices.Sorted(maps.Keys(view.metadata.APIs)) {
		if apiIndex > 0 {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		if err := encoder.Encode(apiName); err != nil {
			return err
		}
		if _, err := io.WriteString(w, ":"); err != nil {
			return err
		}
		api := view.metadata.APIs[apiName]
		if err := writeJSONObjectPrefix(w, storageAPIHeader{APISnapshot: &api}); err != nil {
			return err
		}
		if _, err := io.WriteString(w, `,"models":{`); err != nil {
			return err
		}
		for modelIndex, modelName := range slices.Sorted(maps.Keys(api.Models)) {
			if modelIndex > 0 {
				if _, err := io.WriteString(w, ","); err != nil {
					return err
				}
			}
			if err := encoder.Encode(modelName); err != nil {
				return err
			}
			if _, err := io.WriteString(w, ":"); err != nil {
				return err
			}
			model := api.Models[modelName]
			if err := writeJSONObjectPrefix(w, storageModelHeader{ModelSnapshot: &model}); err != nil {
				return err
			}
			if blocks := view.archived[storageSnapshotModelKey{apiName, modelName}]; len(blocks) > 0 {
				if _, err := io.WriteString(w, `,"accounting":[`); err != nil {
					return err
				}
				first := true
				for _, block := range blocks {
					for _, record := range block {
						if !first {
							if _, err := io.WriteString(w, ","); err != nil {
								return err
							}
						}
						first = false
						archivedDetail = record.detail()
						if err := encoder.Encode(&archivedDetail); err != nil {
							return err
						}
					}
				}
				if _, err := io.WriteString(w, "]"); err != nil {
					return err
				}
			}
			if _, err := io.WriteString(w, `,"details":[`); err != nil {
				return err
			}
			for index := range model.Details {
				if index > 0 {
					if _, err := io.WriteString(w, ","); err != nil {
						return err
					}
				}
				if err := encoder.Encode(&model.Details[index]); err != nil {
					return err
				}
			}
			if _, err := io.WriteString(w, "]}"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, "}}"); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "}}")
	return err
}

func writeStorageSnapshotViewFile(dir string, view storageSnapshotView, now time.Time) error {
	return writeStorageSnapshotAtomically(dir, func(w io.Writer) error { return view.write(w, now) })
}

func writeStorageSnapshotAtomically(dir string, encode func(io.Writer) error) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".snapshot-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	writer := bufio.NewWriterSize(file, 64<<10)
	if err = encode(writer); err == nil {
		err = writer.Flush()
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, storageSnapshotPath(dir)); err != nil {
		return err
	}
	_ = syncDir(dir) // match the existing platform-dependent directory-sync policy
	return nil
}
