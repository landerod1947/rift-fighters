package main

// "subir": manda las partidas ONLINE de save/rf_partidas.jsonl al servidor de estadisticas.
// Lo llama el juego al terminar cada pelea online (sin ventana) y el lanzador al abrirse.
// El servidor es una base Supabase (tabla "reportes"); su direccion y su clave PUBLICA van en
// rf_servidor.json. Esa clave es la "anon / publishable": solo permite agregar reportes y leer
// las estadisticas, no borrar ni cambiar nada.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type ServerCfg struct {
	URL string `json:"url"` // https://xxxx.supabase.co
	Key string `json:"key"` // clave publica (anon / publishable)
}

func loadServer(root string) (ServerCfg, bool) {
	var c ServerCfg
	b, err := os.ReadFile(filepath.Join(root, "rf_servidor.json"))
	if err != nil {
		return c, false
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if json.Unmarshal(b, &c) != nil {
		return c, false
	}
	c.URL = strings.TrimRight(strings.TrimSpace(c.URL), "/")
	c.Key = strings.TrimSpace(c.Key)
	ok := strings.HasPrefix(c.URL, "https://") || strings.HasPrefix(c.URL, "http://127.0.0.1")
	return c, ok && c.Key != "" && !strings.Contains(c.Key, "PEGA")
}

// apodo publico del jugador (nunca se usa el nombre de usuario de Windows)
func loadNick(root string) string {
	b, _ := os.ReadFile(filepath.Join(root, "save", "rf_apodo.txt"))
	return cleanNick(string(b))
}

func cleanNick(s string) string {
	s = strings.TrimSpace(s)
	var out []rune
	for _, r := range s {
		if r >= 32 && r != '<' && r != '>' && r != '"' && r != '\\' {
			out = append(out, r)
		}
		if len(out) >= 20 {
			break
		}
	}
	return strings.TrimSpace(string(out))
}

func saveNick(root, nick string) error {
	_ = os.MkdirAll(filepath.Join(root, "save"), 0755)
	return os.WriteFile(filepath.Join(root, "save", "rf_apodo.txt"), []byte(cleanNick(nick)), 0644)
}

func sharingOff(root string) bool {
	_, err := os.Stat(filepath.Join(root, "save", "rf_no_compartir.txt"))
	return err == nil
}

type report struct {
	MatchID string          `json:"match_id"`
	Lado    int             `json:"lado"`
	Jugador string          `json:"jugador"`
	Version int             `json:"version"`
	P1      json.RawMessage `json:"p1"`
	P2      json.RawMessage `json:"p2"`
	Ganador int             `json:"ganador"`
}

type localRec struct {
	ID      string          `json:"id"`
	Online  bool            `json:"online"`
	Lado    int             `json:"lado"`
	Version json.RawMessage `json:"version"`
	P1      json.RawMessage `json:"p1"`
	P2      json.RawMessage `json:"p2"`
	Ganador int             `json:"ganador"`
}

// devuelve cuantas se mandaron y cuantas quedan pendientes
func uploadPending(root string) (int, int, error) {
	srv, ok := loadServer(root)
	if !ok {
		return 0, 0, fmt.Errorf("sin servidor configurado")
	}
	if sharingOff(root) {
		return 0, 0, nil
	}
	// un solo envio a la vez
	lock := filepath.Join(root, "save", "rf_subiendo.lock")
	if st, err := os.Stat(lock); err == nil && time.Since(st.ModTime()) < 2*time.Minute {
		return 0, 0, nil
	}
	_ = os.WriteFile(lock, []byte("1"), 0644)
	defer os.Remove(lock)

	sentPath := filepath.Join(root, "save", "rf_partidas_enviadas.txt")
	sent := map[string]bool{}
	if b, err := os.ReadFile(sentPath); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				sent[l] = true
			}
		}
	}
	f, err := os.Open(filepath.Join(root, "save", "rf_partidas.jsonl"))
	if err != nil {
		return 0, 0, nil
	}
	defer f.Close()
	nick := loadNick(root)
	if nick == "" {
		nick = "Anonimo"
	}
	var todo []report
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var r localRec
		if json.Unmarshal(sc.Bytes(), &r) != nil || !r.Online || r.ID == "" || (r.Lado != 1 && r.Lado != 2) {
			continue
		}
		key := r.ID + "|" + fmt.Sprint(r.Lado)
		if sent[key] {
			continue
		}
		ver := 0
		_ = json.Unmarshal(bytes.Trim(r.Version, `"`), &ver)
		todo = append(todo, report{MatchID: r.ID, Lado: r.Lado, Jugador: nick, Version: ver, P1: r.P1, P2: r.P2, Ganador: r.Ganador})
	}
	if len(todo) == 0 {
		return 0, 0, nil
	}
	cl := &http.Client{Timeout: 15 * time.Second}
	done := 0
	out, _ := os.OpenFile(sentPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if out != nil {
		defer out.Close()
	}
	// de uno en uno: si el otro jugador ya mando esa pelea, el servidor responde 409 (repetido) y solo
	// se salta esa, sin perder las demas
	for _, r := range todo {
		body, _ := json.Marshal(r)
		req, _ := http.NewRequest("POST", srv.URL+"/rest/v1/reportes", bytes.NewReader(body))
		req.Header.Set("apikey", srv.Key)
		req.Header.Set("Authorization", "Bearer "+srv.Key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Prefer", "return=minimal")
		resp, err := cl.Do(req)
		if err != nil {
			return done, len(todo) - done, err
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode != 409 {
			return done, len(todo) - done, fmt.Errorf("el servidor respondio %d", resp.StatusCode)
		}
		if out != nil {
			fmt.Fprintf(out, "%s|%d\n", r.MatchID, r.Lado)
		}
		done++
	}
	return done, 0, nil
}

func nickOK(s string) bool { return utf8.RuneCountInString(cleanNick(s)) >= 2 }
