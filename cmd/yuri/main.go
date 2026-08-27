// Command yuri is the macOS-first desktop application's entry point.
package main

import (
	"context"
	"embed"
	"log"

	"github.com/OrdoAI/yuri-agent/internal/desktop"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	bridge, err := desktop.NewBridge(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	err = wails.Run(&options.App{
		Title:            "Yuri",
		Width:            1280,
		Height:           800,
		MinWidth:         960,
		MinHeight:        640,
		BackgroundColour: &options.RGBA{R: 15, G: 13, B: 24, A: 1},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        bridge.Startup,
		OnShutdown:       bridge.Shutdown,
		Bind:             []interface{}{bridge},
	})
	if err != nil {
		log.Fatal(err)
	}
}
