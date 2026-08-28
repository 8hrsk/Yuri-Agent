// Command yuri is the macOS-first desktop application's entry point.
package main

import (
	"context"
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	launchSmoke, err := launchSmokeOptionsFromEnvironment()
	if err != nil {
		log.Fatal(err)
	}
	bridge, err := newBridge(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	app := &options.App{
		Title:     "Yuri",
		Width:     1280,
		Height:    800,
		MinWidth:  960,
		MinHeight: 640,
		// Keep the local worker alive when the owner closes the window. A real
		// application quit still runs Shutdown and releases all durable leases.
		HideWindowOnClose: true,
		BackgroundColour:  &options.RGBA{R: 15, G: 13, B: 24, A: 1},
		AssetServer:       &assetserver.Options{Assets: assets},
		OnStartup:         bridge.Startup,
		OnShutdown:        bridge.Shutdown,
		Bind:              []interface{}{bridge},
	}
	var readyResult chan error
	if launchSmoke.enabled() {
		startupComplete := make(chan struct{})
		readyResult = make(chan error, 1)
		app.OnStartup = func(ctx context.Context) {
			bridge.Startup(ctx)
			close(startupComplete)
		}
		// DomReady is the launch-smoke boundary: it proves that the real Wails
		// frontend/WebKit loaded, not merely that Go constructed a Bridge.
		app.OnDomReady = func(ctx context.Context) {
			<-startupComplete
			readyResult <- launchSmoke.writeReady(bridge.Health())
			if launchSmoke.autoExit {
				wailsruntime.Quit(ctx)
			}
		}
	}
	err = wails.Run(app)
	if err != nil {
		log.Fatal(err)
	}
	if readyResult != nil {
		if err := <-readyResult; err != nil {
			log.Fatal(err)
		}
	}
}
