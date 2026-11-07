// Package client embeds the Linux bootstrap that gg-setup installs next to
// the SteelSeries GG Electron client.
package client

import "embed"

//go:embed linux/*.js
var Linux embed.FS
