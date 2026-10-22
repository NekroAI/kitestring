// Package version 保存构建时注入的版本信息。
package version

// 由 -ldflags "-X github.com/NekroAI/kitestring/internal/version.Version=..." 注入。
var (
	Version = "dev"
	Commit  = "unknown"
)
