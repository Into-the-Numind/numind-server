package seed

import "embed"

//go:embed data/*.json
var OpinionData embed.FS
