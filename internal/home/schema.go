package home

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ingot-agent/ingot/internal/image"
)

const HomeSchemaVersion = 2

type schemaFile struct {
	SchemaVersion int       `json:"schema_version"`
	CreatedAt     time.Time `json:"created_at"`
}

func OpenForInit(root string) (*Home, error) {
	home, err := openPath(root)
	if err != nil {
		return nil, err
	}
	info, statErr := os.Stat(home.Root)
	if os.IsNotExist(statErr) {
		return home, nil
	}
	if statErr != nil {
		return nil, statErr
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("INGOT-HOME-SCHEMA-PATH: %s is not a directory", home.Root)
	}
	entries, err := os.ReadDir(home.Root)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return home, nil
	}
	if err := home.validateSchema(); err != nil {
		return nil, err
	}
	release, err := home.acquire(context.Background())
	if err != nil {
		return nil, err
	}
	defer release()
	if err := home.recoverHomeTransactions(); err != nil {
		return nil, err
	}
	if err := home.validateSchema(); err != nil {
		return nil, err
	}
	return home, nil
}

func (home *Home) validateSchema() error {
	data, err := os.ReadFile(filepath.Join(home.Root, "home.json"))
	if err != nil {
		return fmt.Errorf("INGOT-HOME-SCHEMA-MISSING: %s is not an initialized M2 home; move it aside or run ingot init on an empty home: %w", home.Root, err)
	}
	var schema schemaFile
	if err := image.StrictDecode(data, &schema); err != nil {
		return fmt.Errorf("INGOT-HOME-SCHEMA-INVALID: %w", err)
	}
	if schema.SchemaVersion != HomeSchemaVersion || schema.CreatedAt.IsZero() {
		return fmt.Errorf("INGOT-HOME-SCHEMA-VERSION: want %d, got %d", HomeSchemaVersion, schema.SchemaVersion)
	}
	if _, err := image.LoadCatalog(home.CatalogPath()); err != nil {
		return fmt.Errorf("INGOT-IMAGE-CATALOG-OPEN: %w", err)
	}
	for _, legacy := range []string{"current", "current.previous", "state"} {
		if _, err := os.Stat(filepath.Join(home.Root, legacy)); err == nil {
			return fmt.Errorf("INGOT-HOME-SCHEMA-LEGACY: incompatible legacy path %s", filepath.Join(home.Root, legacy))
		}
	}
	return nil
}

func (home *Home) initializeSchema() error {
	if err := home.ensure(); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(home.Root, "home.json")); err == nil {
		return home.validateSchema()
	} else if !os.IsNotExist(err) {
		return err
	}
	entries, err := os.ReadDir(home.Root)
	if err != nil {
		return err
	}
	allowed := map[string]bool{"profiles": true, "images": true, "cache": true, "runtimes": true, ".transactions": true}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fmt.Errorf("INGOT-HOME-SCHEMA-NONEMPTY: %s contains incompatible entry %s", home.Root, entry.Name())
		}
	}
	if err := image.WriteCatalog(home.CatalogPath(), image.NewCatalog()); err != nil {
		return err
	}
	schema := schemaFile{SchemaVersion: HomeSchemaVersion, CreatedAt: time.Now().UTC()}
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return err
	}
	return image.AtomicWrite(filepath.Join(home.Root, "home.json"), append(data, '\n'), 0o600)
}

func (home *Home) CatalogPath() string { return filepath.Join(home.Root, "images", "catalog.json") }
