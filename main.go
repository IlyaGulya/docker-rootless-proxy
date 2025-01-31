package main

import (
	"docker-socket-router/config"
	"docker-socket-router/router"
	"docker-socket-router/version"
	"flag"
	"fmt"
	"go.uber.org/fx/fxevent"

	"go.uber.org/fx"
	"go.uber.org/zap"
)

func main() {
	versionFlag := flag.Bool("version", false, "Print version information and exit")
	verbose := flag.Bool("verbose", false, "Enable verbose logging")
	flag.Parse()

	if *versionFlag {
		fmt.Println(version.Info())
		return
	}

	app := fx.New(
		fx.Provide(
			config.ProvideConfig,
			zap.NewProduction,
			router.NewRouter,
		),
		fx.WithLogger(func(logger *zap.Logger) fxevent.Logger {
			if *verbose {
				return &fxevent.ZapLogger{
					Logger: logger,
				}
			} else {
				return fxevent.NopLogger
			}
		}),
		fx.Invoke(func(lc fx.Lifecycle, router *router.Router) error {
			return router.Start(lc)
		}),
	)

	app.Run()
}
