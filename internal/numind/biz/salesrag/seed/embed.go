package seed

import "embed"

//go:embed data/*.txt
var OpinionData embed.FS
