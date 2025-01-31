package config

type SocketConfig struct {
	SystemSocket         string
	RootlessSocketFormat string
}

func ProvideConfig() *SocketConfig {
	return &SocketConfig{
		SystemSocket:         "/var/run/docker.sock",
		RootlessSocketFormat: "/run/user/%d/docker.sock",
	}
}
