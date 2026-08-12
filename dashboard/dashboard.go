// Package dashboard embeds the static dashboard files and registers them with the server.
package dashboard

import (
	"embed"
	"io/fs"
	"net/http"

	"Zeus/server"
)

//go:embed static
var staticFiles embed.FS

// Register mounts the embedded dashboard at / on the given server.
func Register(srv *server.Server) error {
	// Strip the "static/" prefix so index.html is served at /
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return err
	}
	srv.RegisterStatic(http.FileServer(http.FS(sub)))
	return nil
}
