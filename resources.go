package blooper

import "embed"

// Resources are bundled into the desktop runtime; installation needs no source checkout.
//
//go:embed agents/*.yaml static/* internal/ui/templates/*
var Resources embed.FS
