package main

// Lanzador de Rift Fighters.
//   RiftFighters.exe                      abre el lanzador (ventana propia con Edge en modo app)
//   RiftFighters.exe publicar -version N [-anterior manifest_vN-1.json] [-salida carpeta]
//                                         arma una version nueva para GitHub Releases
// No usa librerias externas: se compila con "go build" para Windows, macOS y Linux.

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

//go:embed ui.html
var uiHTML []byte

//go:embed icon.png
var iconPNG []byte

const launcherVersion = "1.0"

func gameRoot(flagDir string) string {
	if flagDir != "" {
		a, _ := filepath.Abs(flagDir)
		return a
	}
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

func loadConfig(root string) Config {
	cfg := Config{Repo: "USUARIO/rift-fighters", Exe: "Ikemen_GO.exe"}
	if b, err := os.ReadFile(filepath.Join(root, "rf_lanzador.json")); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	if cfg.Exe == "" {
		cfg.Exe = "Ikemen_GO.exe"
	}
	return cfg
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "subir" {
		fs := flag.NewFlagSet("subir", flag.ExitOnError)
		dir := fs.String("juego", "", "carpeta del juego")
		_ = fs.Parse(os.Args[2:])
		root := gameRoot(*dir)
		time.Sleep(2 * time.Second) // deja que el juego termine de escribir la partida
		n, left, err := uploadPending(root)
		if err != nil {
			fmt.Println("subir:", err, "- enviadas:", n, "pendientes:", left)
			os.Exit(1)
		}
		fmt.Println("subir: enviadas", n)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "publicar" {
		fs := flag.NewFlagSet("publicar", flag.ExitOnError)
		dir := fs.String("juego", "", "carpeta del juego (por defecto, la del lanzador)")
		ver := fs.Int("version", 0, "numero de la version nueva")
		prev := fs.String("anterior", "", "manifest_vN.json de la version anterior (vacio = paquete completo)")
		out := fs.String("salida", "", "carpeta donde se dejan los paquetes")
		exe := fs.String("exe", "Ikemen_GO.exe", "ejecutable del juego")
		_ = fs.Parse(os.Args[2:])
		if *ver <= 0 {
			fmt.Println("Falta -version N")
			os.Exit(2)
		}
		root := gameRoot(*dir)
		o := *out
		if o == "" {
			o = filepath.Join(filepath.Dir(root), fmt.Sprintf("RF_publicar_v%d", *ver))
		}
		if err := publish(root, *ver, *prev, o, *exe); err != nil {
			fmt.Println("ERROR:", err)
			os.Exit(1)
		}
		return
	}
	dir := flag.String("juego", "", "carpeta del juego")
	noWin := flag.Bool("sinventana", false, "no abrir la ventana (pruebas)")
	port := flag.Int("puerto", 0, "puerto local (0 = cualquiera)")
	flag.Parse()
	root := gameRoot(*dir)
	cfg := loadConfig(root)
	u := newUpdater(root, cfg)
	runServer(u, *port, !*noWin)
}

func runServer(u *Updater, port int, openWin bool) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		fmt.Println("No se pudo abrir el lanzador:", err)
		os.Exit(1)
	}
	url := fmt.Sprintf("http://%s/", ln.Addr().String())
	var lastPing atomic.Int64
	lastPing.Store(time.Now().Unix())
	token := fmt.Sprintf("%x", time.Now().UnixNano())

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(strings.Replace(string(uiHTML), "{{TOKEN}}", token, 1)))
	})
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(iconPNG)
	})
	mux.HandleFunc("/api/estado", func(w http.ResponseWriter, r *http.Request) {
		lastPing.Store(time.Now().Unix())
		s := u.St.snapshot()
		w.Header().Set("Content-Type", "application/json")
		_, srvOK := loadServer(u.Root)
		json.NewEncoder(w).Encode(map[string]any{"status": s, "discord": u.Cfg.Discord != "", "web": u.Cfg.Web != "",
			"launcher": launcherVersion, "repo": u.Cfg.Repo, "nick": loadNick(u.Root), "share": !sharingOff(u.Root), "server": srvOK})
	})
	action := func(name string, f func()) {
		mux.HandleFunc("/api/"+name, func(w http.ResponseWriter, r *http.Request) {
			// solo la ventana del lanzador puede pedir acciones (evita que una pagina web externa las dispare)
			if r.Method != "POST" || r.Header.Get("X-RF-Token") != token {
				http.Error(w, "no", http.StatusForbidden)
				return
			}
			go f()
			w.Write([]byte("ok"))
		})
	}
	// apodo publico y permiso para compartir partidas online
	mux.HandleFunc("/api/apodo", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("X-RF-Token") != token {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		var body struct {
			Nick  string `json:"nick"`
			Share bool   `json:"share"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body) != nil {
			http.Error(w, "mal", http.StatusBadRequest)
			return
		}
		_ = saveNick(u.Root, body.Nick)
		off := filepath.Join(u.Root, "save", "rf_no_compartir.txt")
		if body.Share {
			os.Remove(off)
		} else {
			_ = os.WriteFile(off, []byte("1"), 0644)
		}
		w.Write([]byte("ok"))
	})
	action("revisar", func() { u.Check(false) })
	action("verificar", func() { u.Check(true) })
	action("actualizar", func() { u.Update() })
	action("jugar", func() {
		s := u.St.snapshot()
		if s.Busy || !s.CanPlay {
			return
		}
		cmd := exec.Command(u.exePath())
		cmd.Dir = u.Root
		if err := cmd.Start(); err != nil {
			u.St.set("error", "No se pudo abrir el juego: "+err.Error(), -1)
			return
		}
		u.St.set("jugando", "Abriendo Rift Fighters...", -1)
		time.Sleep(1500 * time.Millisecond)
		os.Exit(0)
	})
	action("discord", func() { openURL(u.Cfg.Discord) })
	action("web", func() { openURL(u.Cfg.Web) })
	action("salir", func() { time.Sleep(200 * time.Millisecond); os.Exit(0) })

	// al abrir: revisar actualizaciones y mandar partidas online pendientes
	go u.Check(false)
	go uploadPending(u.Root)
	// si la ventana se cierra, el lanzador se apaga solo
	go func() {
		for {
			time.Sleep(3 * time.Second)
			if openWin && time.Now().Unix()-lastPing.Load() > 15 && !u.St.snapshot().Busy {
				os.Exit(0)
			}
		}
	}()
	if openWin {
		go func() {
			time.Sleep(200 * time.Millisecond)
			openWindow(url, u.Root)
		}()
	} else {
		fmt.Println("Lanzador en", url)
	}
	http.Serve(ln, mux)
}

func openWindow(url, root string) {
	if runtime.GOOS == "windows" {
		profile := filepath.Join(root, "save", "rf_lanzador_ventana")
		for _, p := range []string{
			os.Getenv("ProgramFiles(x86)") + `\Microsoft\Edge\Application\msedge.exe`,
			os.Getenv("ProgramFiles") + `\Microsoft\Edge\Application\msedge.exe`,
			os.Getenv("LocalAppData") + `\Google\Chrome\Application\chrome.exe`,
			os.Getenv("ProgramFiles") + `\Google\Chrome\Application\chrome.exe`,
		} {
			if _, err := os.Stat(p); err == nil {
				cmd := exec.Command(p, "--app="+url, "--window-size=940,600", "--user-data-dir="+profile, "--no-first-run")
				if cmd.Start() == nil {
					return
				}
			}
		}
	}
	openURL(url)
}

func openURL(url string) {
	if url == "" {
		return
	}
	switch runtime.GOOS {
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		exec.Command("open", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}
