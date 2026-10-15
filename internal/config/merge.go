package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// parseDoc reads and decodes one config layer. A missing file is not an
// error (exists=false, defaults apply); read and parse failures abort with
// the path in the error (docs/CONFIG.md merge rules).
func parseDoc(path string) (doc map[string]any, exists bool, err error) {
	if path == "" {
		return nil, false, nil
	}

	raw, readErr := os.ReadFile(path) //nolint:gosec // golem's own config path, not user-supplied input
	if errors.Is(readErr, fs.ErrNotExist) {
		return nil, false, nil
	}

	if readErr != nil {
		return nil, false, fmt.Errorf("config: reading %s: %w", path, readErr)
	}

	doc = map[string]any{}
	if err = toml.Unmarshal(raw, &doc); err != nil {
		return nil, false, fmt.Errorf("config: parsing %s: %w", path, err)
	}

	return doc, true, nil
}

// mergeDoc deep-merges src into dst: nested tables merge recursively;
// every other value — slices included — is replaced, never concatenated
// (docs/CONFIG.md merge rules, ADR-0012).
func mergeDoc(dst, src map[string]any) {
	for key, srcVal := range src {
		srcTable, srcIsTable := srcVal.(map[string]any)
		if !srcIsTable {
			dst[key] = srcVal

			continue
		}

		dstTable, dstIsTable := dst[key].(map[string]any)
		if !dstIsTable {
			dstTable = map[string]any{}
			dst[key] = dstTable
		}

		mergeDoc(dstTable, srcTable)
	}
}

// rejectPlaintextKeys fails a committable project file that carries a
// literal api_key: project configs must indirect secrets through ref:
// (ADR-0015). The global file keeps plaintext keys as its designed
// default.
func rejectPlaintextKeys(doc map[string]any, path string) error {
	providerTable, ok := doc["provider"].(map[string]any)
	if !ok {
		return nil
	}

	for id, val := range providerTable {
		table, ok := val.(map[string]any)
		if !ok {
			continue // scalar keys such as provider.default
		}

		key := stringValue(table["api_key"])
		if key == "" || strings.HasPrefix(key, "ref:") {
			continue
		}

		return fmt.Errorf(
			"config: %s: [provider.%s] api_key: %w — project files are committable; use api_key = \"ref:ENV_VAR\"",
			path, id, ErrPlaintextKey,
		)
	}

	return nil
}
