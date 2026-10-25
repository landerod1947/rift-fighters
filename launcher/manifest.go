package main

// Manifiesto de una version del juego: lista de archivos con su hash y en que
// paquete (zip publicado en GitHub Releases) vive la version actual de cada uno.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type FileEntry struct {
	Path   string `json:"path"`           // ruta relativa con "/" (ej. chars/Goku/Goku.def)
	Size   int64  `json:"size"`           // bytes
	SHA256 string `json:"sha256"`         // hash del contenido
	Pack   string `json:"pack"`           // "<tag>/<archivo.zip>" donde esta esta version del archivo
	Keep   bool   `json:"keep,omitempty"` // solo se entrega si el jugador no lo tiene (ej. sus ajustes)
}

type Manifest struct {
	Game    string      `json:"game"`
	Version int         `json:"version"`
	Created string      `json:"created"`
	Exe     string      `json:"exe"` // ejecutable del juego (relativo)
	Files   []FileEntry `json:"files"`
	Deleted []string    `json:"deleted,omitempty"` // archivos que ya no son parte del juego
}

func loadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) save(path string) error {
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	b, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

func (m *Manifest) index() map[string]FileEntry {
	out := make(map[string]FileEntry, len(m.Files))
	for _, f := range m.Files {
		out[strings.ToLower(f.Path)] = f
	}
	return out
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// ruta segura dentro de la carpeta del juego (evita "../" en manifiestos o zips)
func safeJoin(root, rel string) (string, bool) {
	rel = filepath.FromSlash(rel)
	if filepath.IsAbs(rel) || strings.Contains(rel, ":") {
		return "", false
	}
	p := filepath.Join(root, rel)
	r, err := filepath.Rel(root, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}
