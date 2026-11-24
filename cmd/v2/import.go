package main

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Agent-Native-Inc/v2/internal/store"
	"github.com/Agent-Native-Inc/v2/internal/walk"
)

func runImport(dir string, files []string, as string) error {
	dir, err := findProject(dir)
	if err != nil {
		return err
	}
	if as != "" && len(files) > 1 {
		return errors.New("--as needs exactly one file")
	}
	assets, err := readAssets(dir)
	if err != nil {
		return err
	}
	for _, src := range files {
		name := cmp.Or(as, filepath.Base(src))
		if name != filepath.Base(name) || name == "." || name == ".." {
			return fmt.Errorf("--as %q must be a plain file name, no folders", as)
		}
		binary, err := walk.IsBinary(src)
		if err != nil {
			return err
		}
		if !binary {
			return fmt.Errorf("%s is text, and %s; copy it into src/ instead", src, toData)
		}

		key := "assets/imported/" + name
		a, err := importOne(dir, src, key)
		if err != nil {
			return err
		}
		assets[key] = a
		// Write after every file, so a failure on file 3 still keeps files 1 and 2 recorded.
		if err := writeJSON(filepath.Join(dir, assetsFile), assets); err != nil {
			return err
		}
		fmt.Println("imported", key)
	}
	return nil
}

func importOne(dir, src, key string) (asset, error) {
	info, err := os.Stat(src)
	if err != nil {
		return asset{}, err
	}
	if !info.Mode().IsRegular() {
		return asset{}, fmt.Errorf("%s is not a file", src)
	}
	dest := filepath.Join(dir, key)
	if _, err := os.Stat(dest); err == nil {
		same, err := sameBytes(src, dest)
		if err != nil {
			return asset{}, err
		}
		if !same {
			return asset{}, fmt.Errorf("%s already exists with different content; pick another name with --as <name>", key)
		}
		sum, err := store.Put(objectsDir(dir), dest)
		return asset{sum, info.Size(), "imported"}, err
	}

	// Bring it in under a temp name and only rename once it's stored,
	// so a failed import never leaves a half-copied file at dest.
	var sum string
	err = store.Place(dest, func(tmp string) error {
		if err := store.Clone(src, tmp); err != nil {
			return fmt.Errorf("import %s: %w", src, err)
		}
		sum, err = store.Put(objectsDir(dir), tmp)
		return err
	})
	return asset{sum, info.Size(), "imported"}, err
}

func sameBytes(a, b string) (bool, error) {
	infoA, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	infoB, err := os.Stat(b)
	if err != nil || infoA.Size() != infoB.Size() {
		return false, err
	}
	sumA, err := store.Hash(a)
	if err != nil {
		return false, err
	}
	sumB, err := store.Hash(b)
	return sumA == sumB, err
}
