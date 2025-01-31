package main

type SocketConfig struct {
	SystemSocket         string
	RootlessSocketFormat string
}

var defaultConfig = SocketConfig{
	SystemSocket:         "/var/run/docker.sock",
	RootlessSocketFormat: "/run/user/%d/docker.sock",
}
