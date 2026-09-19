//go:build !embedded

package main

// Диспетчер статики dev-сборки (тест-план P3.13, кейсы S1–S3): пустой путь —
// именованная ошибка; непустой — тот же LoadFile, что и раньше (мета равна
// побитово); недоступный файл — ошибка сохраняет контекст пути.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/udisondev/l2go/internal/artifact"
)

func TestLoadStaticDevRequiresPath(t *testing.T) {
	t.Parallel()
	_, _, _, err := loadStatic("")
	if err == nil {
		t.Fatal("loadStatic(\"\"): ожидалась ошибка пустого пути")
	}
	msg := err.Error()
	if !strings.Contains(msg, "артефакт") || !strings.Contains(msg, "обязателен") {
		t.Fatalf("ошибка без именованного текста (мусор mmap вместо guard): %v", err)
	}
}

func TestLoadStaticDevLoadsFile(t *testing.T) {
	t.Parallel()
	st, gm, meta, err := loadStatic(synthArtifact)
	if err != nil {
		t.Fatalf("loadStatic(%s): %v", synthArtifact, err)
	}
	if st == nil || gm == nil {
		t.Fatalf("loadStatic: статика или гео nil (in-band пустой источник)")
	}
	_, _, want, _, werr := artifact.LoadFile(synthArtifact)
	if werr != nil {
		t.Fatalf("LoadFile-эталон: %v", werr)
	}
	if *meta != *want {
		t.Fatalf("мета разошлась с LoadFile: got %+v, want %+v", *meta, *want)
	}
}

func TestLoadStaticDevMissingFileKeepsPathContext(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "нет-такого.l2a")
	_, _, _, err := loadStatic(missing)
	if err == nil {
		t.Fatal("loadStatic(несуществующий): ожидалась ошибка")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("ошибка без переданного пути %s: %v", missing, err)
	}
}
