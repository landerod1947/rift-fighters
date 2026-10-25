package main

// Cache de hashes: para no recalcular el SHA-256 de todo el juego cada vez que se abre
// el lanzador. Si el tamano y la fecha del archivo no cambiaron, se usa el hash guardado.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type cacheEntry struct {
	Size  int64  `json:"s"`
	Mtime int64  `json:"t"`
	Hash  string `json:"h"`
}

type HashCache struct {
	mu   sync.Mutex
	path string
	m    map[string]cacheEntry
}

func openCache(root string) *HashCache {
	c := &HashCache{path: filepath.Join(root, "save", "rf_lanzador_cache.json"), m: map[string]cacheEntry{}}
	if b, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(b, &c.m)
	}
	return c
}

func (c *HashCache) save() {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = os.MkdirAll(filepath.Dir(c.path), 0755)
	if b, err := json.Marshal(c.m); err == nil {
		_ = os.WriteFile(c.path, b, 0644)
	}
}

// hash del archivo; full=true ignora la cache (verificacion completa)
func (c *HashCache) hash(root, rel string, full bool) (string, int64, bool) {
	p, ok := safeJoin(root, rel)
	if !ok {
		return "", 0, false
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return "", 0, false
	}
	key := strings.ToLower(rel)
	c.mu.Lock()
	e, have := c.m[key]
	c.mu.Unlock()
	if have && !full && e.Size == st.Size() && e.Mtime == st.ModTime().UnixNano() {
		return e.Hash, e.Size, true
	}
	h, n, err := hashFile(p)
	if err != nil {
		return "", 0, false
	}
	c.mu.Lock()
	c.m[key] = cacheEntry{Size: n, Mtime: st.ModTime().UnixNano(), Hash: h}
	c.mu.Unlock()
	return h, n, true
}
