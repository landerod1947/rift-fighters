package main

// "publicar": arma una version nueva para subir a GitHub Releases.
//  - Sin manifiesto anterior: paquetes base con todo el juego.
//  - Con manifiesto anterior: un paquete solo con lo nuevo o cambiado (lo demas sigue
//    apuntando a los paquetes de versiones anteriores, que se quedan publicados).

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxPack = int64(1800) << 20 // GitHub acepta archivos de hasta 2 GB por release

// carpetas y archivos que nunca se publican
var excludePrefixes = []string{"save/", "rf_motor/", "_quitados_por_actualizacion/", ".git/", "rf_parches/"}
var excludeNames = []string{"thumbs.db", "desktop.ini", "ikemen.log", "rf_limpieza.bat", "rf_limpieza.ps1", "rf_limpieza_log.txt",
	"rf_parche.ps1", "rf_parche_base.bat", "rf_parche_crear.bat", "rf_actualizar.ps1", "rf_actualizar_log.txt", "ikemen_go_original.exe",
	"para_claude_code.md", "rf_version.txt", "riftfighters.exe", "riftfighters_publicar.exe"}

// archivos que se entregan solo si el jugador no los tiene (sus ajustes no se pisan)
var keepFiles = []string{"save/config.json"}

func excluded(rel string) bool {
	l := strings.ToLower(rel)
	if strings.HasPrefix(l, "respaldo") {
		return true
	}
	for _, p := range excludePrefixes {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	base := l[strings.LastIndex(l, "/")+1:]
	for _, n := range excludeNames {
		if base == n {
			return true
		}
	}
	return strings.HasSuffix(l, ".rfnuevo") || strings.HasSuffix(l, ".log")
}

func groupOf(rel string) string {
	parts := strings.SplitN(rel, "/", 3)
	if len(parts) == 1 {
		return "raiz"
	}
	top := strings.ToLower(parts[0])
	if top == "chars" && len(parts) == 3 {
		c := strings.ToUpper(parts[1][:1])
		if c < "A" || c > "Z" {
			c = "0"
		}
		return "chars_" + c
	}
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, top)
	return safe
}

type pendingFile struct {
	entry FileEntry
	abs   string
}

func publish(gameDir string, version int, prevPath, outDir, exe string) error {
	var prev *Manifest
	if prevPath != "" {
		p, err := loadManifest(prevPath)
		if err != nil {
			return fmt.Errorf("no se pudo leer el manifiesto anterior: %v", err)
		}
		if p.Version >= version {
			return fmt.Errorf("la version nueva (%d) tiene que ser mayor que la anterior (%d)", version, p.Version)
		}
		prev = p
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	tag := fmt.Sprintf("v%d", version)
	m := &Manifest{Game: "Rift Fighters", Version: version, Created: time.Now().Format("2006-01-02 15:04"), Exe: exe}
	var prevIdx map[string]FileEntry
	if prev != nil {
		prevIdx = prev.index()
	}
	var todo []pendingFile
	seen := map[string]bool{}
	fmt.Println("Calculando hashes...")
	err := filepath.Walk(gameDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(gameDir, p)
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			if rel != "." && excluded(rel+"/") {
				for _, k := range keepFiles {
					if strings.HasPrefix(strings.ToLower(k), strings.ToLower(rel)+"/") {
						return nil // carpeta excluida que tiene un archivo de "solo si falta"
					}
				}
				return filepath.SkipDir
			}
			return nil
		}
		if excluded(rel) {
			for _, k := range keepFiles {
				if strings.EqualFold(rel, k) {
					goto keep
				}
			}
			return nil
		}
	keep:
		h, n, err := hashFile(p)
		if err != nil {
			return err
		}
		fe := FileEntry{Path: rel, Size: n, SHA256: h}
		for _, k := range keepFiles {
			if strings.EqualFold(rel, k) {
				fe.Keep = true
			}
		}
		seen[strings.ToLower(rel)] = true
		if prev != nil {
			if old, ok := prevIdx[strings.ToLower(rel)]; ok && strings.EqualFold(old.SHA256, h) && old.Pack != "" {
				fe.Pack = old.Pack
				m.Files = append(m.Files, fe)
				return nil
			}
		}
		todo = append(todo, pendingFile{fe, p})
		return nil
	})
	if err != nil {
		return err
	}
	if prev != nil {
		for _, f := range prev.Files {
			if !seen[strings.ToLower(f.Path)] {
				m.Deleted = append(m.Deleted, f.Path)
			}
		}
		for _, d := range prev.Deleted {
			if !seen[strings.ToLower(d)] {
				m.Deleted = append(m.Deleted, d)
			}
		}
		m.Deleted = uniq(m.Deleted)
	}
	// armar paquetes
	groups := map[string][]pendingFile{}
	for _, t := range todo {
		g := "cambios"
		if prev == nil {
			g = "base_" + groupOf(t.entry.Path)
		}
		groups[g] = append(groups[g], t)
	}
	var names []string
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	var packFiles []string
	for _, g := range names {
		list := groups[g]
		sort.Slice(list, func(i, j int) bool { return list[i].entry.Path < list[j].entry.Path })
		var chunk []pendingFile
		var size int64
		k := 1
		flush := func() error {
			if len(chunk) == 0 {
				return nil
			}
			name := fmt.Sprintf("%s_%s_%d.zip", tag, g, k)
			if err := writePack(filepath.Join(outDir, name), chunk); err != nil {
				return err
			}
			for _, c := range chunk {
				c.entry.Pack = tag + "/" + name
				m.Files = append(m.Files, c.entry)
			}
			packFiles = append(packFiles, name)
			fmt.Printf("  %s: %d archivos, %s\n", name, len(chunk), human(size))
			chunk, size = nil, 0
			k++
			return nil
		}
		for _, t := range list {
			if size+t.entry.Size > maxPack && len(chunk) > 0 {
				if err := flush(); err != nil {
					return err
				}
			}
			chunk = append(chunk, t)
			size += t.entry.Size
		}
		if err := flush(); err != nil {
			return err
		}
	}
	if err := m.save(filepath.Join(outDir, "manifest.json")); err != nil {
		return err
	}
	// copia del manifiesto con su version, para la siguiente publicacion
	_ = m.save(filepath.Join(outDir, fmt.Sprintf("manifest_v%d.json", version)))
	var total int64
	for _, f := range m.Files {
		total += f.Size
	}
	notes := fmt.Sprintf(`Version %d lista en esta carpeta.

Archivos del juego: %d (%s)
Paquetes nuevos: %d
Archivos quitados respecto a la version anterior: %d

Para publicarla en GitHub (pagina del repositorio > Releases > Draft a new release):
  - Tag: %s
  - Sube TODOS los archivos de esta carpeta (los .zip y manifest.json).
  - Publica la release (no la marques como pre-release).
O con la herramienta gh, desde esta carpeta:
  gh release create %s *.zip manifest.json --title "Rift Fighters v%d"

IMPORTANTE: no borres las releases anteriores; el manifiesto nuevo sigue usando sus paquetes.
Guarda manifest_v%d.json: lo necesitas para publicar la siguiente version.
`, version, len(m.Files), human(total), len(packFiles), len(m.Deleted), tag, tag, version, version)
	_ = os.WriteFile(filepath.Join(outDir, "LEEME_PUBLICAR.txt"), []byte(strings.ReplaceAll(notes, "\n", "\r\n")), 0644)
	fmt.Print(notes)
	return nil
}

func uniq(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[strings.ToLower(x)] {
			seen[strings.ToLower(x)] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func writePack(path string, files []pendingFile) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, t := range files {
		hdr := &zip.FileHeader{Name: t.entry.Path, Method: zip.Deflate}
		ext := strings.ToLower(filepath.Ext(t.entry.Path))
		switch ext {
		case ".ogg", ".mp3", ".png", ".zip", ".jpg", ".webm", ".mp4":
			hdr.Method = zip.Store
		}
		hdr.Modified = time.Now()
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		src, err := os.Open(t.abs)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, src)
		src.Close()
		if err != nil {
			return err
		}
	}
	return zw.Close()
}
