package main

import (
	"docker-socket-router/config"
	"docker-socket-router/router"
	"docker-socket-router/version"
	"flag"
	"fmt"

	"go.uber.org/fx"
	"go.uber.org/zap"
)

func main() {
	versionFlag := flag.Bool("version", false, "Print version information and exit")
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
		fx.Invoke(func(lc fx.Lifecycle, router *router.Router) error {
			return router.Start(lc)
		}),
	)

	app.Run()
}
