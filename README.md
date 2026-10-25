# Rift Fighters

Juego de peleas gratuito hecho por fans sobre Ikemen GO (version con rollback).
Este repositorio guarda las **herramientas** del juego; los archivos del juego en si se publican
como paquetes en **Releases** (no van dentro del repositorio porque pesan varios GB).

## Carpetas
- `launcher/` - lanzador (Go, sin librerias externas). Revisa actualizaciones, verifica los archivos
  con SHA-256, descarga solo lo que cambio y abre el juego. Tambien trae el comando `publicar`.
- `web/` - pagina de estadisticas (winrate y pickrate por personaje, quien mainea a cada uno).
  Por ahora lee los archivos `save/rf_partidas.jsonl` que genera el juego.

## Compilar el lanzador
```
cd launcher
GOOS=windows GOARCH=amd64 go build -ldflags "-H windowsgui -s -w" -o RiftFighters.exe .
GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" -o RiftFighters_publicar.exe .   (version de consola para publicar)
GOOS=darwin  GOARCH=arm64 go build -o RiftFighters_mac .
GOOS=linux   GOARCH=amd64 go build -o RiftFighters_linux .
```

## Publicar una version
1. Con el juego cerrado, correr `rf_motor\publicar\publicar.bat` (o `RiftFighters_publicar.exe publicar -version N -anterior manifest_vN-1.json`).
2. Crear una Release con el tag `vN` y subir todos los archivos de la carpeta `RF_publicar_vN` (los .zip y `manifest.json`).
3. No borrar releases anteriores: el manifiesto nuevo reutiliza sus paquetes.

## Licencias
El motor Ikemen GO es MIT (ver License.txt del juego). Los recursos de Elecbyte son CC BY-NC (no comercial).
Ver `RF_LICENCIAS.txt` en la carpeta del juego.
