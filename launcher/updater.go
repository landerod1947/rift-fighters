package main

// Revisar, verificar y actualizar el juego a partir del manifiesto publicado.

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Repo        string `json:"repo"`        // "usuario/repositorio" en GitHub
	ManifestURL string `json:"manifestUrl"` // si se deja vacio: releases/latest/download/manifest.json
	PackBaseURL string `json:"packBaseUrl"` // si se deja vacio: https://github.com/<repo>/releases/download
	Discord     string `json:"discord"`
	Web         string `json:"web"`
	Exe         string `json:"exe"`
}

func (c Config) manifestURL() string {
	if c.ManifestURL != "" {
		return c.ManifestURL
	}
	return "https://github.com/" + c.Repo + "/releases/latest/download/manifest.json"
}

func (c Config) packURL(pack string) string {
	base := c.PackBaseURL
	if base == "" {
		base = "https://github.com/" + c.Repo + "/releases/download"
	}
	return strings.TrimRight(base, "/") + "/" + pack
}

// estado que ve la pantalla del lanzador
type Status struct {
	mu          *sync.Mutex
	Phase       string  `json:"phase"` // listo, revisando, descargando, verificando, error, sinconexion, actualizado
	Message     string  `json:"message"`
	Progress    float64 `json:"progress"` // 0..1, -1 = sin barra
	LocalVer    string  `json:"localVersion"`
	RemoteVer   int     `json:"remoteVersion"`
	NeedFiles   int     `json:"needFiles"`
	NeedBytes   int64   `json:"needBytes"`
	Busy        bool    `json:"busy"`
	CanPlay     bool    `json:"canPlay"`
	Installed   bool    `json:"installed"`
	Configured  bool    `json:"configured"`
	LastChecked string  `json:"lastChecked"`
}

func (s *Status) set(phase, msg string, prog float64) {
	s.mu.Lock()
	s.Phase, s.Message, s.Progress = phase, msg, prog
	s.mu.Unlock()
}

func (s *Status) snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{mu: s.mu, Phase: s.Phase, Message: s.Message, Progress: s.Progress, LocalVer: s.LocalVer, RemoteVer: s.RemoteVer,
		NeedFiles: s.NeedFiles, NeedBytes: s.NeedBytes, Busy: s.Busy, CanPlay: s.CanPlay, Installed: s.Installed,
		Configured: s.Configured, LastChecked: s.LastChecked}
}

type Updater struct {
	Root   string
	Cfg    Config
	St     *Status
	Cache  *HashCache
	remote *Manifest
	need   []FileEntry
	client *http.Client
	busyMu sync.Mutex
}

func newUpdater(root string, cfg Config) *Updater {
	u := &Updater{Root: root, Cfg: cfg, St: &Status{mu: &sync.Mutex{}, Phase: "listo", Progress: -1}, Cache: openCache(root),
		client: &http.Client{Timeout: 0}}
	u.St.Configured = cfg.Repo != "" && !strings.Contains(cfg.Repo, "USUARIO")
	u.refreshLocal()
	return u
}

func (u *Updater) exePath() string {
	exe := u.Cfg.Exe
	if exe == "" {
		exe = "Ikemen_GO.exe"
	}
	p, _ := safeJoin(u.Root, exe)
	return p
}

func (u *Updater) refreshLocal() {
	b, _ := os.ReadFile(filepath.Join(u.Root, "rf_version.txt"))
	v := strings.TrimSpace(string(b))
	if v == "" {
		v = "0"
	}
	_, err := os.Stat(u.exePath())
	u.St.mu.Lock()
	u.St.LocalVer = v
	u.St.Installed = err == nil
	u.St.CanPlay = err == nil
	u.St.mu.Unlock()
}

func (u *Updater) begin() bool {
	u.busyMu.Lock()
	defer u.busyMu.Unlock()
	u.St.mu.Lock()
	defer u.St.mu.Unlock()
	if u.St.Busy {
		return false
	}
	u.St.Busy = true
	return true
}

func (u *Updater) end() {
	u.St.mu.Lock()
	u.St.Busy = false
	u.St.mu.Unlock()
	u.Cache.save()
	u.refreshLocal()
}

func (u *Updater) fetchManifest() (*Manifest, error) {
	req, _ := http.NewRequest("GET", u.Cfg.manifestURL(), nil)
	req.Header.Set("User-Agent", "RiftFightersLauncher")
	cl := &http.Client{Timeout: 20 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("el servidor respondio %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "rfman*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, io.LimitReader(resp.Body, 64<<20)); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()
	return loadManifest(tmp.Name())
}

// compara los archivos locales con el manifiesto; full = recalcular todos los hashes
func (u *Updater) diff(m *Manifest, full bool, label string) []FileEntry {
	var need []FileEntry
	total := len(m.Files)
	for i, f := range m.Files {
		if i%25 == 0 {
			u.St.set("verificando", fmt.Sprintf("%s (%d de %d archivos)", label, i, total), float64(i)/float64(max(1, total)))
		}
		if f.Keep {
			if p, ok := safeJoin(u.Root, f.Path); ok {
				if _, err := os.Stat(p); err == nil {
					continue
				}
			}
		}
		h, _, ok := u.Cache.hash(u.Root, f.Path, full)
		if !ok || !strings.EqualFold(h, f.SHA256) {
			need = append(need, f)
		}
	}
	return need
}

func (u *Updater) Check(full bool) {
	if !u.begin() {
		return
	}
	defer u.end()
	if !u.St.Configured {
		u.St.set("sinconfig", "Las actualizaciones automaticas todavia no estan activadas. Puedes jugar normal.", -1)
		return
	}
	u.St.set("revisando", "Buscando actualizaciones...", -1)
	m, err := u.fetchManifest()
	if err != nil {
		u.St.set("sinconexion", "No se pudo revisar si hay actualizaciones: "+err.Error()+". Puedes jugar igual.", -1)
		return
	}
	u.remote = m
	label := "Revisando tus archivos"
	if full {
		label = "Verificando todos los archivos"
	}
	need := u.diff(m, full, label)
	var bytes int64
	for _, f := range need {
		bytes += f.Size
	}
	u.need = need
	u.St.mu.Lock()
	u.St.RemoteVer = m.Version
	u.St.NeedFiles = len(need)
	u.St.NeedBytes = bytes
	u.St.LastChecked = time.Now().Format("15:04")
	u.St.mu.Unlock()
	if len(need) == 0 {
		u.writeVersion(m.Version)
		u.St.set("actualizado", fmt.Sprintf("Todo al dia (version %d).", m.Version), -1)
	} else if !u.St.Installed {
		u.St.set("instalar", fmt.Sprintf("Hay que descargar el juego: %d archivos, %s.", len(need), human(bytes)), -1)
	} else {
		u.St.set("pendiente", fmt.Sprintf("Version %d disponible: %d archivos por descargar (%s).", m.Version, len(need), human(bytes)), -1)
	}
}

func (u *Updater) writeVersion(v int) {
	_ = os.WriteFile(filepath.Join(u.Root, "rf_version.txt"), []byte(strconv.Itoa(v)), 0644)
}

// descarga e instala lo que falte
func (u *Updater) Update() {
	if !u.begin() {
		return
	}
	defer u.end()
	if u.remote == nil {
		u.St.set("error", "Primero hay que revisar actualizaciones.", -1)
		return
	}
	if err := u.apply(u.remote, u.need); err != nil {
		u.St.set("error", "La actualizacion fallo: "+err.Error()+". Vuelve a intentar.", -1)
		return
	}
	// archivos que ya no son parte del juego: se apartan (no se borran)
	moved := 0
	for _, d := range u.remote.Deleted {
		p, ok := safeJoin(u.Root, d)
		if !ok {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			dst, _ := safeJoin(u.Root, filepath.ToSlash(filepath.Join("_quitados_por_actualizacion", d)))
			_ = os.MkdirAll(filepath.Dir(dst), 0755)
			if os.Rename(p, dst) == nil {
				moved++
			}
		}
	}
	need := u.diff(u.remote, false, "Comprobando lo descargado")
	u.need = need
	u.St.mu.Lock()
	u.St.NeedFiles = len(need)
	u.St.mu.Unlock()
	if len(need) > 0 {
		u.St.set("error", fmt.Sprintf("Quedaron %d archivos sin actualizar. Vuelve a intentar.", len(need)), -1)
		return
	}
	u.writeVersion(u.remote.Version)
	msg := fmt.Sprintf("Listo: version %d instalada.", u.remote.Version)
	if moved > 0 {
		msg += fmt.Sprintf(" %d archivos viejos se movieron a _quitados_por_actualizacion.", moved)
	}
	u.St.set("actualizado", msg, -1)
}

func (u *Updater) apply(m *Manifest, need []FileEntry) error {
	if len(need) == 0 {
		return nil
	}
	byPack := map[string][]FileEntry{}
	var packs []string
	var total int64
	for _, f := range need {
		if f.Pack == "" {
			return errors.New("el manifiesto no dice donde esta " + f.Path)
		}
		if _, ok := byPack[f.Pack]; !ok {
			packs = append(packs, f.Pack)
		}
		byPack[f.Pack] = append(byPack[f.Pack], f)
	}
	sort.Strings(packs)
	tmpDir := filepath.Join(u.Root, "save", "rf_descargas")
	_ = os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)
	for i, pk := range packs {
		label := fmt.Sprintf("Descargando paquete %d de %d", i+1, len(packs))
		zp := filepath.Join(tmpDir, strings.ReplaceAll(pk, "/", "_"))
		if err := u.download(u.Cfg.packURL(pk), zp, label); err != nil {
			return fmt.Errorf("%s: %v", pk, err)
		}
		u.St.set("instalando", fmt.Sprintf("Instalando paquete %d de %d", i+1, len(packs)), -1)
		if err := u.extract(zp, byPack[pk]); err != nil {
			return fmt.Errorf("%s: %v", pk, err)
		}
		os.Remove(zp)
		_ = total
	}
	return nil
}

func (u *Updater) download(url, dst, label string) error {
	var lastErr error
	for try := 0; try < 3; try++ {
		lastErr = u.downloadOnce(url, dst, label)
		if lastErr == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return lastErr
}

func (u *Updater) downloadOnce(url, dst, label string) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "RiftFightersLauncher")
	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("el servidor respondio %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	size := resp.ContentLength
	var done int64
	buf := make([]byte, 256<<10)
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if time.Since(last) > 150*time.Millisecond {
				last = time.Now()
				p := -1.0
				if size > 0 {
					p = float64(done) / float64(size)
				}
				u.St.set("descargando", fmt.Sprintf("%s: %s de %s", label, human(done), human(size)), p)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if size > 0 && done != size {
		return errors.New("descarga incompleta")
	}
	return nil
}

// saca del zip solo los archivos necesarios, revisa su hash y los pone en su lugar
func (u *Updater) extract(zipPath string, files []FileEntry) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	want := map[string]FileEntry{}
	for _, f := range files {
		want[strings.ToLower(f.Path)] = f
	}
	found := 0
	for _, zf := range zr.File {
		key := strings.ToLower(strings.ReplaceAll(zf.Name, "\\", "/"))
		fe, ok := want[key]
		if !ok {
			continue
		}
		dst, ok := safeJoin(u.Root, fe.Path)
		if !ok {
			return errors.New("ruta no valida en el paquete: " + fe.Path)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		tmp := dst + ".rfnuevo"
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(tmp)
		if err != nil {
			rc.Close()
			return err
		}
		_, cerr := io.Copy(out, rc)
		rc.Close()
		out.Close()
		if cerr != nil {
			os.Remove(tmp)
			return cerr
		}
		h, _, err := hashFile(tmp)
		if err != nil || !strings.EqualFold(h, fe.SHA256) {
			os.Remove(tmp)
			return errors.New("el archivo descargado no coincide: " + fe.Path)
		}
		_ = os.Remove(dst)
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("no se pudo reemplazar %s (¿el juego esta abierto?)", fe.Path)
		}
		u.Cache.hash(u.Root, fe.Path, true)
		found++
	}
	if found != len(files) {
		return fmt.Errorf("al paquete le faltan %d archivos", len(files)-found)
	}
	return nil
}

func human(n int64) string {
	if n < 0 {
		return "?"
	}
	const k = 1024
	switch {
	case n >= k*k*k:
		return fmt.Sprintf("%.1f GB", float64(n)/(k*k*k))
	case n >= k*k:
		return fmt.Sprintf("%.1f MB", float64(n)/(k*k))
	case n >= k:
		return fmt.Sprintf("%.0f KB", float64(n)/k)
	}
	return fmt.Sprintf("%d B", n)
}
