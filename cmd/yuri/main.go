// Command yuri is the macOS-first desktop application's entry point.
package main

import (
	"context"
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed ui_smoke_onboarding.js
var uiSmokeOnboardingScript string

//go:embed ui_smoke_voice.js
var uiSmokeVoiceScript string

// applicationMenu keeps the standard macOS App and Edit menus (without one the
// window has no Cut/Copy/Paste shortcuts at all) and adds the View entry that
// carries the ⌃⌘F fullscreen accelerator.
func applicationMenu() *menu.Menu {
	view := menu.NewMenu()
	view.AddText("Полноэкранный режим", keys.Combo("f", keys.CmdOrCtrlKey, keys.ControlKey), func(_ *menu.CallbackData) {
		if fullscreenContext == nil {
			return
		}
		if wailsruntime.WindowIsFullscreen(fullscreenContext) {
			wailsruntime.WindowUnfullscreen(fullscreenContext)
			return
		}
		wailsruntime.WindowFullscreen(fullscreenContext)
	})
	return menu.NewMenuFromItems(menu.AppMenu(), menu.EditMenu(), menu.SubMenu("Вид", view), menu.WindowMenu())
}

// fullscreenContext is the Wails runtime context the menu callback needs. The
// menu is built before Wails starts, so the context can only be captured once
// startup hands it over.
var fullscreenContext context.Context

func main() {
	launchSmoke, err := launchSmokeOptionsFromEnvironment()
	if err != nil {
		log.Fatal(err)
	}
	bridge, err := newBridge(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	startup := func(ctx context.Context) {
		fullscreenContext = ctx
		bridge.Startup(ctx)
	}
	app := &options.App{
		Title:     "Yuri",
		Width:     1280,
		Height:    800,
		MinWidth:  820,
		MinHeight: 600,
		// Keep the local worker alive when the owner closes the window. A real
		// application quit still runs Shutdown and releases all durable leases.
		HideWindowOnClose: true,
		BackgroundColour:  &options.RGBA{R: 15, G: 13, B: 24, A: 1},
		AssetServer:       &assetserver.Options{Assets: assets},
		OnStartup:         startup,
		OnShutdown:        bridge.Shutdown,
		Bind:              []interface{}{bridge},
		Menu:              applicationMenu(),
		Mac: &mac.Options{
			// Wails only reads the zoom preference from Mac options: leaving
			// this struct nil left `zoomable` false, which disables the green
			// window button and with it the whole native fullscreen path.
			DisableZoom: false,
			// Escape denies a pending approval (ApprovalDialog). In fullscreen
			// the system would otherwise swallow that key to leave fullscreen,
			// so a refusal would silently turn into a window state change.
			DisableEscapeExitsFullscreen: true,
		},
	}
	var readyResult chan error
	var uiSmokeReporter *UISmokeReporter
	if launchSmoke.enabled() {
		startupComplete := make(chan struct{})
		readyResult = make(chan error, 1)
		app.OnStartup = func(ctx context.Context) {
			startup(ctx)
			close(startupComplete)
		}
		if launchSmoke.uiFlow != "" {
			uiSmokeReporter = newUISmokeReporter(launchSmoke)
			app.Bind = append(app.Bind, uiSmokeReporter)
		}
		// DomReady is the launch-smoke boundary: it proves that the real Wails
		// frontend/WebKit loaded, not merely that Go constructed a Bridge.
		app.OnDomReady = func(ctx context.Context) {
			<-startupComplete
			readyResult <- launchSmoke.writeReady(bridge.Health())
			if uiSmokeReporter != nil {
				uiSmokeReporter.attach(ctx)
				wailsruntime.WindowExecJS(ctx, launchSmoke.uiScript())
			} else if launchSmoke.autoExit {
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
