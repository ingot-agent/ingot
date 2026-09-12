package home

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ingot-agent/ingot/internal/image"
	processmgr "github.com/ingot-agent/ingot/internal/process"
)

const homeTransactionVersion = 1

type homeTransaction struct {
	TransactionVersion int                       `json:"transaction_version"`
	Kind               string                    `json:"kind"`
	Phase              string                    `json:"phase"`
	ImageImport        *imageImportTransaction   `json:"image_import,omitempty"`
	RuntimeDelete      *runtimeDeleteTransaction `json:"runtime_delete,omitempty"`
}

type imageImportTransaction struct {
	Binding image.Binding `json:"binding"`
	Tag     image.Source  `json:"tag"`
}

type runtimeDeleteTransaction struct {
	Name  string `json:"name"`
	Purge bool   `json:"purge"`
}

func (home *Home) transactionDirectory() string {
	return filepath.Join(home.Root, ".transactions")
}

func (home *Home) writeHomeTransaction(transaction homeTransaction) (string, error) {
	id, err := processmgr.NewID()
	if err != nil {
		return "", err
	}
	transaction.TransactionVersion = homeTransactionVersion
	transaction.Phase = "prepared"
	data, err := json.MarshalIndent(transaction, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	path := filepath.Join(home.transactionDirectory(), id+".json")
	if err := image.AtomicWrite(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (home *Home) removeHomeTransaction(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(home.transactionDirectory())
}

func (home *Home) recoverHomeTransactions() error {
	entries, err := os.ReadDir(home.transactionDirectory())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		info, infoErr := entry.Info()
		name := entry.Name()
		id := strings.TrimSuffix(name, ".json")
		if infoErr != nil || !info.Mode().IsRegular() || !strings.HasSuffix(name, ".json") || processmgr.ValidateID(id) != nil {
			return fmt.Errorf("INGOT-HOME-TRANSACTION-ENTRY: invalid transaction entry %s", entry.Name())
		}
		if err := home.recoverHomeTransaction(filepath.Join(home.transactionDirectory(), entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (home *Home) recoverHomeTransaction(path string) error {
	data, err := readFileBounded(path, image.MaxJSONSize)
	if err != nil {
		return err
	}
	var transaction homeTransaction
	if err := image.StrictDecode(data, &transaction); err != nil {
		return fmt.Errorf("INGOT-HOME-TRANSACTION-SCHEMA: %w", err)
	}
	if transaction.TransactionVersion != homeTransactionVersion || transaction.Phase != "prepared" {
		return fmt.Errorf("INGOT-HOME-TRANSACTION-SCHEMA: invalid version or phase")
	}
	switch transaction.Kind {
	case "image_import":
		if transaction.ImageImport == nil || transaction.RuntimeDelete != nil {
			return fmt.Errorf("INGOT-HOME-TRANSACTION-SCHEMA: invalid image import payload")
		}
		payload := transaction.ImageImport
		if err := payload.Binding.Validate(); err != nil {
			return err
		}
		if err := image.ValidateName(payload.Tag.Name); err != nil {
			return err
		}
		if err := image.ValidateTag(payload.Tag.Tag); err != nil {
			return err
		}
		if payload.Binding.Source == nil || *payload.Binding.Source != payload.Tag {
			return fmt.Errorf("INGOT-HOME-TRANSACTION-SCHEMA: import source differs from tag")
		}
		directory := home.imageDirectory(payload.Binding.ImageID)
		verified, verifyErr := image.Verify(directory, payload.Binding.ImageID, nil)
		if verifyErr == nil {
			if verified.Manifest.ArtifactDigest != payload.Binding.ArtifactDigest || !verified.Manifest.Target.Equal(payload.Binding.Target) {
				return fmt.Errorf("INGOT-HOME-TRANSACTION-IMAGE: committed image differs from journal")
			}
			catalog, err := image.LoadCatalog(home.CatalogPath())
			if err != nil {
				return err
			}
			if err := catalog.Set(payload.Tag, payload.Binding); err != nil {
				return err
			}
			if err := image.WriteCatalog(home.CatalogPath(), catalog); err != nil {
				return err
			}
		} else if _, statErr := os.Stat(directory); !os.IsNotExist(statErr) {
			return verifyErr
		}
	case "runtime_delete":
		if transaction.RuntimeDelete == nil || transaction.ImageImport != nil {
			return fmt.Errorf("INGOT-HOME-TRANSACTION-SCHEMA: invalid runtime delete payload")
		}
		payload := transaction.RuntimeDelete
		if err := image.ValidateRuntimeName(payload.Name); err != nil {
			return err
		}
		if err := os.RemoveAll(home.registry().RuntimeHome(payload.Name)); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Join(home.Root, "runtimes")); err != nil {
			return err
		}
	default:
		return fmt.Errorf("INGOT-HOME-TRANSACTION-KIND: unsupported kind %q", transaction.Kind)
	}
	return home.removeHomeTransaction(path)
}

func readFileBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("INGOT-HOME-TRANSACTION-SIZE: %s exceeds limit", path)
	}
	return os.ReadFile(path)
}
